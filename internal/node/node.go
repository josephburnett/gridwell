// Package node is the embeddable Gridwell node: everything `gridwell serve`
// does between config in hand and listener up, as a library — the home store,
// the transport, plugin loading, the server assembly, and the lifecycle. The
// CLI is the wrapper, not the wiring: it adds flags, the serve lock, the
// banner, and signal handling around this, and reimplements none of the
// middle.
//
// The node is its home: one id qualifies the home store ("<id>/12") and every
// connection through this node ("<id>/<conn>/…"). The store and the transport
// are constructed here, by the node, from its own config; they are not plugins
// and never appear in `plugins:`.
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
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginmeta"
	"github.com/josephburnett/gridwell/internal/remote"
	"github.com/josephburnett/gridwell/internal/remote/dial"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/sourcecache"
)

// BuildConfig loads server.yaml at cfgPath and prepares it for launch. A
// missing file is a fresh home, so an empty config. Any absent id, the node's
// own or a plugin's, is minted and the file written back: the one config
// write the node ever makes. Every plugin gets its derived db_file injected,
// never stored in the config; the store is created on first serve; the web
// password is read or minted; and CacheDir is derived for the source cache.
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
	// The web door is never open: the password is the web-password file
	// beside the config, minted here on first serve, printed by serve, and
	// rotated by deleting it.
	if cfg.WebPassword, err = config.EnsurePasswordFile(home); err != nil {
		return nil, err
	}
	if cfg.Federation.Socket == "" {
		cfg.Federation.Socket = config.FederationSocket(home)
	}
	// The source cache lives beside the DB, never inside it: disposable and
	// excluded from backup.
	cfg.CacheDir = home
	if err := ensureStore(home, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// ensureStore makes <home>/gridwell.db exist. A fresh home gets one, with its
// identity stamped through pluginmeta; a home laid out as db/<id>/… is
// converted into one by Convert; an existing home is left alone.
//
// gridwell.db existing beside a db/ directory is the one window a conversion
// can be killed in and leave work behind: Convert publishes the finished
// store with a rename and retires db/ with a second one. The store is
// complete — Convert builds into a temp and renames only on success — so the
// answer is to finish the set-aside, never to convert a second time over the
// data the first one already folded.
func ensureStore(home string, cfg *config.ServerConfig) error {
	path := config.DBFile(home)
	if _, err := os.Stat(path); err == nil {
		// An existing store must be this node's: a changed id must never
		// silently open, or shadow, another identity's data.
		if _, err := pluginmeta.Verify(path, cfg.ID, "home"); err != nil {
			return fmt.Errorf("%s is not the store of id %q — did `id` change? (an id is immutable; restore the old one): %w", path, cfg.ID, err)
		}
		if _, err := os.Stat(filepath.Join(home, "db")); err == nil {
			log.Printf("gridwell: %s is converted but the old db/ was never set aside — finishing an interrupted conversion", path)
			if err := setAsideOldLayout(home); err != nil {
				return err
			}
			log.Printf("gridwell: converted; the old files are in %s (delete when satisfied)", filepath.Join(home, "db.pre-one-node"))
		}
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if _, err := os.Stat(filepath.Join(home, "db")); err == nil {
		return Convert(home, cfg)
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	return pluginmeta.Create(path, cfg.ID, "home")
}

// Options configures Start.
type Options struct {
	// Home is the Gridwell home, from the one config.Home() derivation: the
	// store, the cache, and every plugin's state directory hang off it.
	Home string
	// Cfg is the prepared config: BuildConfig plus any caller adjustments,
	// such as the bind, a static override, or a forced DisableShells.
	Cfg *config.ServerConfig
	// StaticFS serves the web client at /; nil disables static files.
	StaticFS fs.FS
}

// Node is a running (or listen-ready) Gridwell node.
type Node struct {
	Reg *plugin.Registry
	// Ln is the web door's listener, bound where web.bind says. FedLn is the
	// federation door's unix socket, and is nil when the door is closed.
	Ln    net.Listener
	FedLn net.Listener

	st            *store.Store
	cache         *sourcecache.Store
	srv           *server.Server
	webSrv        *http.Server
	fedSrv        *http.Server
	cancelRequest context.CancelFunc
	closeOnce     sync.Once
	closeErr      error
}

// Start assembles the node — home, plugins, transport, server — and listens,
// but does not serve yet: the caller announces the bound address first, which
// is the CLI's banner contract, and then calls ServeBackground. On error
// nothing is left running.
func Start(opts Options) (*Node, error) {
	cfg := opts.Cfg
	// The store: home content, every plugin's namespace, and the transport's
	// connections. One file, one handle, identity verified against the node's
	// id.
	st, err := local.OpenVerified(config.DBFile(opts.Home), cfg.ID, "home")
	if err != nil {
		return nil, err
	}
	// The source cache: one disposable file remembering what every non-home
	// source last said. It is opened here, once, and put in front of each
	// namespace below, which is the one place the node decides who is cached
	// and under what policy. Home is never cached: its answers are the
	// durable file.
	cache := openCache(cfg)
	front := func(ns namespace.Namespace, o sourcecache.Options) namespace.Namespace {
		if cache == nil {
			return ns
		}
		return cache.Front(ns, o)
	}
	reg := plugin.NewRegistry()
	fail := func(err error) (*Node, error) {
		reg.Close()
		if cache != nil {
			_ = cache.Close()
		}
		st.Close()
		return nil, err
	}
	// Home first: the first registered entry with a root is where a client
	// lands. See rpc.HomeGrid.
	if err := startHome(reg, st, cfg); err != nil {
		return fail(fmt.Errorf("home: %w", err))
	}
	// A content plugin gets the engine with no prefetch: its source is
	// local to this machine, so a crawl on every reconnect would warm
	// what is already at hand.
	if err := plugin.LoadInto(reg, cfg, opts.Home, st, func(ns namespace.Namespace) namespace.Namespace {
		return front(ns, sourcecache.Options{})
	}); err != nil {
		return fail(fmt.Errorf("load plugins: %w", err))
	}
	// The transport gets the engine with prefetch: a connection's answers
	// cross a network, and offline readability means everything on the far
	// machine, not only what was visited.
	if err := startTransport(reg, st, cfg, func(ns namespace.Namespace) namespace.Namespace {
		return front(ns, sourcecache.Options{Prefetch: true})
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
	// Two doors, two listeners. The web door binds where config says; a
	// tailnet address is fine, because it is password-gated. The federation
	// door is a 0600 unix socket, or closed, and never TCP, so no config can
	// expose the ungated gRPC export to another uid, let alone a network. ssh
	// tunnels terminate on it.
	webSrv := &http.Server{
		Handler:           srv.WebHandler(),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return requestCtx },
	}
	fedSrv := &http.Server{
		Handler:           srv.FederationHandler(),
		Protocols:         server.NodeProtocols(),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return requestCtx },
	}
	ln, err := net.Listen("tcp", cfg.Web.Bind)
	if err != nil {
		cancel()
		return fail(err)
	}
	var fedLn net.Listener
	if sock := cfg.Federation.Socket; sock != "" {
		fedLn, err = listenFederation(sock)
		if err != nil {
			ln.Close()
			cancel()
			return fail(err)
		}
	}
	return &Node{Reg: reg, Ln: ln, FedLn: fedLn, st: st, cache: cache, srv: srv, webSrv: webSrv, fedSrv: fedSrv, cancelRequest: cancel}, nil
}

// startHome registers the home over the store: a Go value the router calls
// directly, with no hop at all. Home does not own the store handle; the node
// opened it and the node closes it.
func startHome(reg *plugin.Registry, st *store.Store, cfg *config.ServerConfig) error {
	reg.Register(cfg.ID, "home", newHome(st, cfg.ID, cfg.Shell), nil)
	reg.SetLabel(cfg.ID, "home")
	return nil
}

// openCache opens the node's one source cache, <home>/cache.db. A cache that
// cannot open degrades to the uncached node, loudly but never fatally: the
// cache is an availability layer, and refusing to serve because it broke would
// invert its purpose.
func openCache(cfg *config.ServerConfig) *sourcecache.Store {
	if cfg.CacheDir == "" {
		return nil
	}
	if err := os.MkdirAll(cfg.CacheDir, 0o700); err != nil {
		log.Printf("gridwell: source cache dir %s: %v (plugins and connections run uncached)", cfg.CacheDir, err)
		return nil
	}
	cache, err := sourcecache.Open(config.CacheFile(cfg.CacheDir))
	if err != nil {
		log.Printf("gridwell: source cache: %v (plugins and connections run uncached)", err)
		return nil
	}
	return cache
}

// startTransport opens the connection store, reconciles it against the
// declared connections, dials them (bounded — the boot doesn't serve
// mysteries), and installs the transport as the node's connection
// namespace ("<id>/<conn>/…"), fronted by the source cache so a dark
// remote degrades to stale-but-readable instead of blank.
func startTransport(reg *plugin.Registry, st *store.Store, cfg *config.ServerConfig, front func(namespace.Namespace) namespace.Namespace) error {
	db, err := remote.NewDB(st.SQL())
	if err != nil {
		return err
	}
	userHome, _ := os.UserHomeDir()
	impl, err := remote.New(db, dial.Dial, userHome, cfg.Connections, cfg.RetiredNames)
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

// closeImpl releases a native impl's own resources (the store's DB, the
// transport's sessions). A close failure at shutdown is reported, never
// fatal — the process is exiting.
func closeImpl(impl any) {
	c, ok := impl.(interface{ Close() error })
	if !ok {
		return
	}
	if err := c.Close(); err != nil {
		log.Printf("gridwell: close: %v", err)
	}
}

// listenFederation opens the federation socket: a stale file from a
// crashed serve is unlinked first (the serve lock guarantees no live
// holder), and the socket is 0600 — the kernel is the gate.
func listenFederation(path string) (net.Listener, error) {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("federation socket: %w", err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("federation socket: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, fmt.Errorf("federation socket: %w", err)
	}
	return ln, nil
}

// ServeBackground starts serving on the listeners (the federation door
// only when open); the returned channel carries the first serve error
// (never http.ErrServerClosed).
func (n *Node) ServeBackground() <-chan error {
	errCh := make(chan error, 2)
	serve := func(s *http.Server, ln net.Listener) {
		if err := s.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}
	go serve(n.webSrv, n.Ln)
	if n.FedLn != nil {
		go serve(n.fedSrv, n.FedLn)
	}
	return errCh
}

// Close shuts the node down: in-flight requests get a bounded drain,
// then the registry (home, plugins, transport) closes. Idempotent by
// contract — the CLI both defers it (every exit path) and calls it
// explicitly (to report the error); the second call is a no-op returning
// the first call's verdict.
func (n *Node) Close() error {
	n.closeOnce.Do(func() {
		n.cancelRequest()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := n.webSrv.Shutdown(ctx)
		if n.FedLn != nil {
			err = errors.Join(err, n.fedSrv.Shutdown(ctx)) // Close unlinks the socket
		}
		// The cache closes BEFORE the sources it fronts: its prefetch
		// walk reads through them and writes into the file, and a walk
		// must be out before either goes away.
		if n.cache != nil {
			err = errors.Join(err, n.cache.Close())
		}
		n.Reg.Close()
		err = errors.Join(err, n.st.Close())
		n.closeErr = err
	})
	return n.closeErr
}
