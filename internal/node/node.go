// Package node is the EMBEDDABLE Gridwell node: everything `gridwell
// serve` does between "config in hand" and "listener up", as a library —
// plugin loading (subprocess or in-process), node identity, the server
// assembly, and the lifecycle. Extracted (offline-plan phase 2) so the
// CLI and the mobile bind (mobile/) share ONE serve wiring instead of
// drifting copies: the CLI adds flags, the serve lock, the banner and
// signal handling around it; mobile adds in-process factories and
// auto-init. Neither reimplements the middle.
package node

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/josephburnett/gridwell/api/idshape"
	"github.com/josephburnett/gridwell/api/pluginmeta"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/server"
)

// BuildConfig loads the mandatory server.yaml at cfgPath and prepares it
// for launch: the file must exist and list at least one plugin (no
// synthesized fallback); every plugin gets its derived db_file injected
// (the path is never stored in the config — fixed at
// <home>/db/<id>/store.db); and CacheDir is derived for the mount cache.
func BuildConfig(home, cfgPath string) (*config.ServerConfig, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("no config at %s; run `gridwell init --kind local --name <name>` to create one", cfgPath)
		}
		return nil, err
	}
	if len(cfg.Plugins) == 0 {
		return nil, fmt.Errorf("%s lists no plugins; run `gridwell init --kind local --name <name>`", cfgPath)
	}
	// The web door is never open (owner decision 2026-08-26): a home
	// without a password does not serve. init mints one, so this only
	// bites a hand-edited file — and says how to fix it.
	if cfg.Web.Password == "" {
		return nil, fmt.Errorf("%s has no web.password; `gridwell init` mints one, or set web.password yourself", cfgPath)
	}
	if cfg.Federation.Socket == "" {
		cfg.Federation.Socket = config.FederationSocket(home)
	}
	// The mount cache lives beside (never inside) the plugin DBs:
	// disposable, excluded from backup, per-mount files under one dir.
	cfg.CacheDir = filepath.Join(home, "cache")
	for i := range cfg.Plugins {
		pc := &cfg.Plugins[i]
		if pc.Config == nil {
			pc.Config = map[string]string{}
		}
		dbFile := config.DBFile(home, pc.ID)
		pc.Config["db_file"] = dbFile
		// The DB must already exist: it is created once by init. serve
		// never creates one — otherwise a changed id (whose derived path
		// doesn't exist) would silently spawn a fresh, empty store
		// instead of failing. EXCEPTION: a Provider entry's derived path
		// is the NODE-owned memory DB (docs/v2-design.md §3.2), which is
		// durable-but-FORGETTABLE by contract — creating it empty is the
		// defined recovery from losing it, so serve creates it freely
		// (layout.OpenVerified stamps identity at creation).
		if pc.Provider {
			if err := os.MkdirAll(filepath.Dir(dbFile), 0o755); err != nil {
				return nil, fmt.Errorf("provider %q (%s): db dir: %w", pc.Name, pc.ID, err)
			}
			continue
		}
		if _, err := os.Stat(dbFile); err != nil {
			return nil, fmt.Errorf("plugin %q (%s): no database at %s; run `gridwell init` to create it", pc.Name, pc.ID, dbFile)
		}
	}
	return cfg, nil
}

// InitPlugin registers one plugin in a home: mint the durable id, create
// the DB with its identity stamped (pluginmeta), append the server.yaml
// entry, and ensure the node's own id. The one init door — the CLI's
// `gridwell init` and mobile's first-run auto-init both come through
// here, so a home initialized on any platform is byte-compatible with
// every other.
func InitPlugin(home, kind, name string, conf map[string]string) (id string, err error) {
	id = idshape.NewShortID()
	dbDir := config.DBDir(home, id)
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		return "", err
	}
	dbFile := config.DBFile(home, id)
	if err := pluginmeta.Create(dbFile, id, kind); err != nil {
		return "", err
	}
	entry := config.PluginConfig{ID: id, Name: name, Kind: kind}
	if len(conf) > 0 {
		entry.Config = conf
	}
	if err := config.AppendPlugin(home, entry); err != nil {
		// The DB dir is keyed by this run's fresh id and referenced by no
		// config entry, so it is safe to remove on failure.
		_ = os.RemoveAll(dbDir)
		return "", err
	}
	// The node's own identity rides in the same file; mint it with the
	// first plugin so a fresh home is fully identified before first serve.
	if _, err := config.EnsureNodeID(home, idshape.NewShortID); err != nil {
		return "", fmt.Errorf("node id: %w", err)
	}
	// The web door is never open: mint the password with the first
	// plugin so a fresh home is gated before first serve (2026-08-26).
	if _, err := config.EnsureWebPassword(home, config.MintPassword); err != nil {
		return "", fmt.Errorf("web password: %w", err)
	}
	return id, nil
}

// Options configures Start.
type Options struct {
	Home string
	// Cfg is the prepared config (BuildConfig + any caller adjustments:
	// bind, static override, forced DisableShells).
	Cfg *config.ServerConfig
	// Factories, when non-nil, provides in-process constructors for
	// plugins whose Binary is empty (the mobile path — iOS forbids
	// fork/exec, so subprocess plugins cannot exist there; the same gRPC
	// surface serves over a loopback port instead, owner decision in
	// docs/offline-plan.md phase 2). The CLI passes nil: production
	// desktop/server plugins are always subprocesses.
	Factories map[string]plugin.ServerFactory
	// ProviderFactories, when non-nil, provides in-process constructors
	// for Provider entries whose Binary is empty (bundled binaries;
	// mobile; tests) — the provider twin of Factories.
	ProviderFactories map[string]plugin.ProviderFactory
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
}

// Start assembles the node — plugins, identity, server — and LISTENS,
// but does not serve yet: the caller announces the bound address first
// (the CLI's banner contract) and then calls ServeBackground. On error
// nothing is left running.
func Start(opts Options) (*Node, error) {
	cfg := opts.Cfg
	reg, err := plugin.LoadAllWithProviders(cfg, opts.Factories, opts.ProviderFactories)
	if err != nil {
		return nil, fmt.Errorf("load plugins: %w", err)
	}
	nodeID, err := config.EnsureNodeID(opts.Home, idshape.NewShortID)
	if err != nil {
		reg.Close()
		return nil, fmt.Errorf("node id: %w", err)
	}
	srv := server.New(reg, server.Config{
		StaticFS: opts.StaticFS,
		NodeID:   nodeID,
		// The landing page's viewport survives restarts in a small state
		// file beside the config ("things stay as you left them").
		NodeStatePath: filepath.Join(opts.Home, "node-view.json"),
		Password:      cfg.Web.Password,
		DisableShells: cfg.DisableShells,
	})
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
// then the plugins close.
func (n *Node) Close() error {
	n.cancelRequest()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := n.webSrv.Shutdown(ctx)
	if n.FedLn != nil {
		err = errors.Join(err, n.fedSrv.Shutdown(ctx)) // Close unlinks the socket
	}
	n.srv.Close()
	n.Reg.Close()
	return err
}

// InitProvider registers a v2 CONTENT PROVIDER entry (docs/v2-design.md):
// like InitPlugin, but no plugin DB is created — the derived db path is
// the NODE-owned memory DB, which serve creates and identity-stamps on
// first load (forgettable by contract, so absence is never an error).
func InitProvider(home, kind, name string, conf map[string]string) (id string, err error) {
	id = idshape.NewShortID()
	if err := os.MkdirAll(config.DBDir(home, id), 0o755); err != nil {
		return "", err
	}
	entry := config.PluginConfig{ID: id, Name: name, Kind: kind, Provider: true}
	if len(conf) > 0 {
		entry.Config = conf
	}
	if err := config.AppendPlugin(home, entry); err != nil {
		return "", err
	}
	if _, err := config.EnsureNodeID(home, idshape.NewShortID); err != nil {
		return "", fmt.Errorf("node id: %w", err)
	}
	// The web door is never open: mint the password with the first
	// plugin so a fresh home is gated before first serve (2026-08-26).
	if _, err := config.EnsureWebPassword(home, config.MintPassword); err != nil {
		return "", fmt.Errorf("web password: %w", err)
	}
	return id, nil
}
