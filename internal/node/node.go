// Package node is the EMBEDDABLE Gridwell node: everything `gridwell
// serve` does between "config in hand" and "listener up", as a library —
// the home store, the transport, plugin loading (subprocess or
// in-process), the server assembly, and the lifecycle. The CLI and the
// mobile bind share this ONE wiring: the CLI adds flags, the serve lock,
// the banner and signal handling around it; mobile adds its bundled plugin
// factories. Neither reimplements the middle.
//
// The node IS its home (docs/one-node.md): one id qualifies the home
// store ("<id>/12") and every connection through this node
// ("<id>/<conn>/…"). The store and the transport are constructed HERE, by
// the node, from its own config — they are not plugins and never appear
// in `plugins:`.
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
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/plugin/mountcache"
	"github.com/josephburnett/gridwell/internal/pluginmeta"
	"github.com/josephburnett/gridwell/internal/remote"
	"github.com/josephburnett/gridwell/internal/remote/dial"
	"github.com/josephburnett/gridwell/internal/server"
)

// BuildConfig loads server.yaml at cfgPath and prepares it for launch. A
// missing file is a fresh home: an empty config. Any absent id (the
// node's own, a plugin's) is minted and the file written back — the one
// config write the node ever makes. Every plugin gets its derived db_file
// injected (never stored in the config), the home and transport stores
// are created on first serve, the web password is read (or minted), and
// CacheDir is derived for the mount cache.
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
	// The web door is never open (owner decision 2026-08-26): the password
	// is the web-password file beside the config, minted here on first
	// serve, printed by serve, rotated by deleting it.
	if cfg.WebPassword, err = config.EnsurePasswordFile(home); err != nil {
		return nil, err
	}
	if cfg.Federation.Socket == "" {
		cfg.Federation.Socket = config.FederationSocket(home)
	}
	// The mount cache lives beside (never inside) the DBs: disposable,
	// excluded from backup.
	cfg.CacheDir = filepath.Join(home, "cache")
	if err := ensureHomeStores(home, cfg); err != nil {
		return nil, err
	}
	for i := range cfg.Plugins {
		pc := &cfg.Plugins[i]
		if pc.Config == nil {
			pc.Config = map[string]string{}
		}
		// A plugin's derived path is the NODE-owned memory DB
		// (docs/v2-design.md §3.2), durable-but-FORGETTABLE by contract —
		// creating it empty is the defined recovery from losing it, so
		// serve creates it freely (layout.OpenVerified stamps identity at
		// creation).
		dbFile := config.DBFile(home, pc.ID)
		pc.Config["db_file"] = dbFile
		if err := os.MkdirAll(filepath.Dir(dbFile), 0o700); err != nil {
			return nil, fmt.Errorf("plugin %q (%s): db dir: %w", pc.Kind, pc.ID, err)
		}
	}
	return cfg, nil
}

// ensureHomeStores creates the home store on a FRESH home (identity
// stamped, pluginmeta). An existing home whose store
// is missing under its id is refused — a changed id must never silently
// spawn an empty store beside the real one. "Existing" = <home>/db holds
// a store this config does not name (a plugin's memory DB it does name is
// fine; a plugin may be listed before first serve).
func ensureHomeStores(home string, cfg *config.ServerConfig) error {
	id := cfg.ID
	homeDB := config.DBFile(home, id)
	if _, err := os.Stat(homeDB); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		known := map[string]bool{}
		for _, p := range cfg.Plugins {
			known[p.ID] = true
		}
		entries, _ := os.ReadDir(filepath.Join(home, "db"))
		for _, e := range entries {
			if e.IsDir() && !known[e.Name()] {
				return fmt.Errorf("no home store at %s, but %s exists — did `id` change? (an id is immutable; restore the old one)", homeDB, config.DBDir(home, e.Name()))
			}
		}
		if err := os.MkdirAll(config.DBDir(home, id), 0o700); err != nil {
			return err
		}
		if err := pluginmeta.Create(homeDB, id, "home"); err != nil {
			return err
		}
	}
	return nil
}

// Options configures Start.
type Options struct {
	Home string
	// Cfg is the prepared config (BuildConfig + any caller adjustments:
	// bind, static override, forced DisableShells).
	Cfg *config.ServerConfig
	// Factories, when non-nil, provides in-process constructors for plugin
	// entries whose Binary is empty (bundled binaries; mobile — iOS forbids
	// fork/exec; tests).
	Factories map[string]plugin.Factory
	// StaticFS serves the web client at /; nil disables static files.
	StaticFS fs.FS
}

// Node is a running (or listen-ready) Gridwell node.
type Node struct {
	Reg *plugin.Registry
	// Ln is the web door's listener (bound where web.bind says); FedLn is
	// the federation door's unix socket, nil when the door is closed.
	Ln    net.Listener
	FedLn net.Listener

	srv           *server.Server
	webSrv        *http.Server
	fedSrv        *http.Server
	cancelRequest context.CancelFunc
	closeOnce     sync.Once
	closeErr      error
}

// Start assembles the node — home, plugins, transport, server — and
// LISTENS, but does not serve yet: the caller announces the bound address
// first (the CLI's banner contract) and then calls ServeBackground. On
// error nothing is left running.
func Start(opts Options) (*Node, error) {
	cfg := opts.Cfg
	reg := plugin.NewRegistry()
	// HOME first: the first registered entry with a root is where a client
	// lands (rpc.HomeGrid).
	if err := startHome(reg, opts.Home, cfg); err != nil {
		reg.Close()
		return nil, fmt.Errorf("home: %w", err)
	}
	if err := plugin.LoadInto(reg, cfg, opts.Factories); err != nil {
		reg.Close()
		return nil, fmt.Errorf("load plugins: %w", err)
	}
	if err := startTransport(reg, opts.Home, cfg); err != nil {
		reg.Close()
		return nil, fmt.Errorf("transport: %w", err)
	}
	srv, err := server.New(reg, server.Config{
		ID:            cfg.ID,
		StaticFS:      opts.StaticFS,
		Password:      cfg.WebPassword,
		DisableShells: cfg.DisableShells,
	})
	if err != nil {
		reg.Close()
		return nil, err
	}
	requestCtx, cancel := context.WithCancel(context.Background())
	// Two doors, two listeners (owner decision 2026-08-26): the web door
	// binds where config says (a tailnet address is fine — it is
	// password-gated); the federation door is a 0600 UNIX SOCKET, or
	// closed — never TCP, so no config can expose the ungated gRPC export
	// to another uid, let alone a network. ssh tunnels terminate on it.
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
		reg.Close()
		return nil, err
	}
	var fedLn net.Listener
	if sock := cfg.Federation.Socket; sock != "" {
		fedLn, err = listenFederation(sock)
		if err != nil {
			ln.Close()
			cancel()
			reg.Close()
			return nil, err
		}
	}
	return &Node{Reg: reg, Ln: ln, FedLn: fedLn, srv: srv, webSrv: webSrv, fedSrv: fedSrv, cancelRequest: cancel}, nil
}

// startHome opens the home store under the node's id and registers it
// (transitional: served over the in-process loopback like a plugin until
// the registry holds Go values — docs/one-node.md P3).
func startHome(reg *plugin.Registry, home string, cfg *config.ServerConfig) error {
	impl, err := NativeLocalFactory(map[string]string{
		"db_file": config.DBFile(home, cfg.ID),
		"uuid":    cfg.ID,
		"kind":    "home",
		"shell":   cfg.Shell,
	})
	if err != nil {
		return err
	}
	client, stop, err := plugin.ServeInProcess(impl)
	if err != nil {
		closeImpl(impl)
		return err
	}
	reg.Register(cfg.ID, "home", client, func() { stop(); closeImpl(impl) })
	reg.SetLabel(cfg.ID, "home")
	return nil
}

// startTransport opens the connection store, reconciles it against the
// declared connections, dials them (bounded — the boot doesn't serve
// mysteries), and installs the transport as the node's connection
// namespace ("<id>/<conn>/…"), fronted by the mount cache so a dark
// remote degrades to stale-but-readable instead of blank.
func startTransport(reg *plugin.Registry, home string, cfg *config.ServerConfig) error {
	db, err := remote.OpenDB(config.RemoteDBFile(home, cfg.ID))
	if err != nil {
		return err
	}
	userHome, _ := os.UserHomeDir()
	impl, err := remote.New(db, dial.Dial, userHome, cfg.Connections, cfg.RetiredNames)
	if err != nil {
		db.Close()
		return err
	}
	impl.ConnectAll(context.Background())
	client, stop, err := plugin.ServeInProcess(impl)
	if err != nil {
		closeImpl(impl)
		return err
	}
	closer := func() { stop(); closeImpl(impl) }
	rows := func(ctx context.Context) []plugin.ConnectionRow {
		out := []plugin.ConnectionRow{}
		for _, r := range impl.Rows(ctx) {
			out = append(out, plugin.ConnectionRow{Name: r.Name, Label: r.Label, RootGridID: r.RootGridID,
				StatusDetail: r.StatusDetail, ViewCx: r.ViewCx, ViewCy: r.ViewCy, ViewZoom: r.ViewZoom})
		}
		return out
	}
	if cfg.CacheDir != "" {
		// A cache that cannot open degrades to the uncached client —
		// loudly, never fatally: the cache is an availability layer, and
		// refusing to serve because the OPTIMIZATION broke would invert
		// its purpose.
		if mkErr := os.MkdirAll(cfg.CacheDir, 0o700); mkErr != nil {
			log.Printf("gridwell: mount cache dir %s: %v (connections run uncached)", cfg.CacheDir, mkErr)
		} else if cached, cacheClose, cErr := mountcache.Open(client, filepath.Join(cfg.CacheDir, cfg.ID+".db")); cErr != nil {
			log.Printf("gridwell: mount cache: %v (connections run uncached)", cErr)
		} else {
			client = cached
			inner := closer
			closer = func() { cacheClose(); inner() }
		}
	}
	reg.SetTransport(client, rows, closer)
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
		n.Reg.Close()
		n.closeErr = err
	})
	return n.closeErr
}
