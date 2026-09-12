// Package node is the embeddable Gridwell node: everything `gridwell serve`
// does between config in hand and listener up. The CLI is a wrapper that adds
// flags, the serve lock, the banner and signal handling, and reimplements none
// of the middle. The store and the transport are constructed here from the
// node's own config, so they never appear in `plugins:`.
package node

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/connection"
	"github.com/josephburnett/gridwell/internal/connection/dial"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginmeta"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/sourcecache"
)

// BuildConfig loads server.yaml at cfgPath and prepares it for launch. A
// missing file is a fresh home. Minting an absent id and writing the file back
// is the one config write the node ever makes.
func BuildConfig(home, cfgPath string) (*config.ServerConfig, error) {
	cfg, err := config.Load(cfgPath)
	if errors.Is(err, fs.ErrNotExist) {
		fresh := config.Defaults
		cfg = &fresh
		err = nil
	}
	if err != nil {
		return nil, err
	}
	if config.Mint(cfg) {
		if err := config.Save(cfgPath, cfg); err != nil {
			return nil, err
		}
	}
	// The web door is never open: the file is the password, and deleting it
	// rotates.
	if cfg.WebPassword, err = config.EnsurePasswordFile(home); err != nil {
		return nil, err
	}
	if cfg.Federation.Socket == "" {
		cfg.Federation.Socket = config.FederationSocket(home)
	}
	// The source cache lives beside the DB, never inside it: disposable.
	cfg.CacheDir = home
	if err := ensureStore(home, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// ensureStore makes <home>/gridwell.db exist, stamped with this node's
// identity. A home still in the retired db/<id>/ layout is refused: its
// content is in those files, and minting an empty store beside them would
// look like a home that lost everything.
func ensureStore(home string, cfg *config.ServerConfig) error {
	path := config.DBFile(home)
	if _, err := os.Stat(path); err == nil {
		// A changed id must never silently open, or shadow, another
		// identity's data.
		if _, err := pluginmeta.Verify(path, cfg.ID, "home"); err != nil {
			return fmt.Errorf("%s is not the store of id %q — did `id` change? (an id is immutable; restore the old one): %w", path, cfg.ID, err)
		}
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if _, err := os.Stat(filepath.Join(home, "db")); err == nil {
		return fmt.Errorf("%s has a db/ directory and no %s: this home is in the layout Gridwell used before one database per node. v0.1.0 is the last release that converts it — serve this home with v0.1.0 once, then with this version", home, path)
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	return pluginmeta.Create(path, cfg.ID, "home")
}

// Options configures Start.
type Options struct {
	// Home is the Gridwell home: store, cache and plugin state hang off it.
	Home string
	// Cfg is BuildConfig's result plus any caller adjustments.
	Cfg *config.ServerConfig
	// StaticFS serves the web client at /; nil disables static files.
	StaticFS fs.FS
}

// Node is a running (or listen-ready) Gridwell node.
type Node struct {
	Reg *plugin.Registry
	// Ln is the web door's listener. ConnLn is the connection door's socket,
	// nil when the door is closed.
	Ln     net.Listener
	ConnLn net.Listener

	st            *store.Store
	cache         *sourcecache.Store
	srv           *server.Server
	webSrv        *http.Server
	connSrv       *http.Server
	cancelRequest context.CancelFunc
	closeOnce     sync.Once
	closeErr      error
}

// Start assembles the node and listens, but does not serve: the caller
// announces the bound address first, which is the CLI's banner contract, and
// then calls ServeBackground. On error nothing is left running.
func Start(opts Options) (*Node, error) {
	cfg := opts.Cfg
	// One store file, one handle, identity verified against the node's id.
	st, err := local.OpenVerified(config.DBFile(opts.Home), cfg.ID, "home")
	if err != nil {
		return nil, err
	}
	// One disposable file remembering what a connection last said, put in
	// front of the transport below and nowhere else: a cache earns its keep
	// across a network, and home and a plugin are both on this machine.
	cache := openCache(cfg)
	reg := plugin.NewRegistry()
	fail := func(err error) (*Node, error) {
		reg.Close()
		_ = cache.Close()
		st.Close()
		return nil, err
	}
	// Home first: the first registered entry with a root is where a client
	// lands. See rpc.HomeGrid.
	if err := startHome(reg, st, cfg); err != nil {
		return fail(fmt.Errorf("home: %w", err))
	}
	// A content plugin is registered bare: its source is a subprocess here, so
	// remembering answers buys no round trip. What it needs is supervision,
	// and internal/plugin respawns one that dies.
	if err := plugin.LoadInto(reg, cfg, opts.Home, st); err != nil {
		return fail(fmt.Errorf("load plugins: %w", err))
	}
	// The transport gets prefetch: offline readability means everything on the
	// far machine, not only what was visited.
	if err := startTransport(reg, st, cfg, func(ns namespace.Namespace) namespace.Namespace {
		return cache.Front(ns, sourcecache.Options{Prefetch: true})
	}); err != nil {
		return fail(fmt.Errorf("transport: %w", err))
	}
	srv, err := server.New(reg, server.Config{
		ID:            cfg.ID,
		StaticFS:      opts.StaticFS,
		Password:      cfg.WebPassword,
		DisableShells: cfg.DisableShells,
	})
	if err != nil {
		return fail(err)
	}
	requestCtx, cancel := context.WithCancel(context.Background())
	// The web door binds where config says; a tailnet address is fine because
	// it is password-gated. The connection door is a 0600 unix socket or
	// closed, never TCP, so no config can expose the ungated gRPC export.
	webSrv := server.WebDoorServer(srv.WebHandler())
	webSrv.BaseContext = func(net.Listener) context.Context { return requestCtx }
	connSrv := server.ConnectionDoorServer(srv.ConnectionHandler())
	connSrv.BaseContext = func(net.Listener) context.Context { return requestCtx }
	ln, err := net.Listen("tcp", cfg.Web.Bind)
	if err != nil {
		cancel()
		return fail(err)
	}
	var connLn net.Listener
	if sock := cfg.Federation.Socket; sock != "" {
		connLn, err = server.ListenConnectionDoor(sock)
		if err != nil {
			ln.Close()
			cancel()
			return fail(err)
		}
	}
	return &Node{Reg: reg, Ln: ln, ConnLn: connLn, st: st, cache: cache, srv: srv, webSrv: webSrv, connSrv: connSrv, cancelRequest: cancel}, nil
}

// startHome registers the home over the store: a Go value the router calls
// directly. The node owns the store handle, not home.
func startHome(reg *plugin.Registry, st *store.Store, cfg *config.ServerConfig) error {
	reg.Register(cfg.ID, "home", newHome(st, cfg.ID, cfg.Shell), nil)
	reg.SetLabel(cfg.ID, "home")
	return nil
}

// openCache opens <home>/cache.db. A cache that cannot open degrades to the
// uncached node, never fatally: refusing to serve because an availability
// layer broke would invert its purpose. It is not silent either — the node
// still answers every read, so the lost serve-first and offline reading would
// show nowhere, and sourcecache.Unavailable puts the reason on the client's
// strip as the transport's health.
func openCache(cfg *config.ServerConfig) *sourcecache.Store {
	cache, err := openCacheFile(cfg)
	if err == nil {
		return cache
	}
	log.Printf("gridwell: source cache: %v (connections run uncached)", err)
	return sourcecache.Unavailable("the source cache could not be opened (" + err.Error() +
		"): what a connection answers is not remembered, so every read waits on the far machine and nothing reads while it is unreachable")
}

// openCacheFile is the cache file, or why there is none.
func openCacheFile(cfg *config.ServerConfig) (*sourcecache.Store, error) {
	if cfg.CacheDir == "" {
		return nil, errors.New("no cache directory is configured")
	}
	if err := os.MkdirAll(cfg.CacheDir, 0o700); err != nil {
		return nil, fmt.Errorf("cache dir %s: %w", cfg.CacheDir, err)
	}
	return sourcecache.Open(config.CacheFile(cfg.CacheDir))
}

// startTransport reconciles the connection store against the declared
// connections, dials them bounded, and installs the transport as the node's
// connection namespace ("<id>/<conn>/…") behind front.
func startTransport(reg *plugin.Registry, st *store.Store, cfg *config.ServerConfig, front func(namespace.Namespace) namespace.Namespace) error {
	db, err := connection.NewDB(st.SQL())
	if err != nil {
		return err
	}
	userHome, _ := os.UserHomeDir()
	impl, err := connection.New(db, dial.Dial, userHome, cfg.Connections, cfg.RetiredNames)
	if err != nil {
		return err
	}
	impl.ConnectAll(context.Background())
	rows := func(ctx context.Context) []plugin.ConnectionRow {
		out := []plugin.ConnectionRow{}
		for _, r := range impl.Rows(ctx) {
			out = append(out, plugin.ConnectionRow{Name: r.Name, Label: r.Label, RootGridID: r.RootGridID,
				StatusDetail: r.StatusDetail, ViewCx: r.ViewCx, ViewCy: r.ViewCy, ViewZoom: r.ViewZoom})
		}
		return out
	}
	reg.SetTransport(front(impl), rows, func() { closeImpl(impl) })
	return nil
}

// closeImpl releases a native impl's own resources. A close failure at
// shutdown is reported, never fatal; the process is exiting.
func closeImpl(impl any) {
	c, ok := impl.(interface{ Close() error })
	if !ok {
		return
	}
	if err := c.Close(); err != nil {
		log.Printf("gridwell: close: %v", err)
	}
}

// ServeBackground starts serving; the returned channel carries the first serve
// error, never http.ErrServerClosed.
func (n *Node) ServeBackground() <-chan error {
	errCh := make(chan error, 2)
	serve := func(s *http.Server, ln net.Listener) {
		if err := s.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}
	go serve(n.webSrv, n.Ln)
	if n.ConnLn != nil {
		go serve(n.connSrv, n.ConnLn)
	}
	return errCh
}

// Close drains in-flight requests, bounded, then closes the registry.
// Idempotent by contract: the CLI both defers it and calls it to report the
// error, and the second call returns the first's verdict.
func (n *Node) Close() error {
	n.closeOnce.Do(func() {
		n.cancelRequest()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := n.webSrv.Shutdown(ctx)
		if n.ConnLn != nil {
			err = errors.Join(err, n.connSrv.Shutdown(ctx)) // Close unlinks the socket
		}
		// The cache closes BEFORE the transport it fronts: its prefetch walk
		// reads through it and must be out before either goes away.
		err = errors.Join(err, n.cache.Close())
		n.Reg.Close()
		err = errors.Join(err, n.st.Close())
		n.closeErr = err
	})
	return n.closeErr
}
