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

	"github.com/josephburnett/gridwell/api/pluginmeta"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/plugins/localdb/store"
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
			return nil, fmt.Errorf("no config at %s; run `gridwell init --kind localdb --name <name>` to create one", cfgPath)
		}
		return nil, err
	}
	if len(cfg.Plugins) == 0 {
		return nil, fmt.Errorf("%s lists no plugins; run `gridwell init --kind localdb --name <name>`", cfgPath)
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
		// instead of failing.
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
	id = store.NewShortID()
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
	if _, err := config.EnsureNodeID(home, store.NewShortID); err != nil {
		return "", fmt.Errorf("node id: %w", err)
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
	// StaticFS serves the web client at /; nil disables static files.
	StaticFS fs.FS
}

// Node is a running (or listen-ready) Gridwell node.
type Node struct {
	Reg *plugin.Registry
	Ln  net.Listener

	httpSrv       *http.Server
	cancelRequest context.CancelFunc
}

// Start assembles the node — plugins, identity, server — and LISTENS,
// but does not serve yet: the caller announces the bound address first
// (the CLI's banner contract) and then calls ServeBackground. On error
// nothing is left running.
func Start(opts Options) (*Node, error) {
	cfg := opts.Cfg
	reg, err := plugin.LoadAll(cfg, opts.Factories)
	if err != nil {
		return nil, fmt.Errorf("load plugins: %w", err)
	}
	nodeID, err := config.EnsureNodeID(opts.Home, store.NewShortID)
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
		Password:      cfg.Password,
		DisableShells: cfg.DisableShells,
	})
	requestCtx, cancel := context.WithCancel(context.Background())
	httpSrv := &http.Server{
		Handler:           srv.NodeHandler(),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return requestCtx },
	}
	ln, err := net.Listen("tcp", cfg.Bind)
	if err != nil {
		cancel()
		reg.Close()
		return nil, err
	}
	return &Node{Reg: reg, Ln: ln, httpSrv: httpSrv, cancelRequest: cancel}, nil
}

// ServeBackground starts serving on the listener; the returned channel
// carries the first serve error (never http.ErrServerClosed).
func (n *Node) ServeBackground() <-chan error {
	errCh := make(chan error, 1)
	go func() {
		if err := n.httpSrv.Serve(n.Ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	return errCh
}

// Close shuts the node down: in-flight requests get a bounded drain,
// then the plugins close.
func (n *Node) Close() error {
	n.cancelRequest()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := n.httpSrv.Shutdown(ctx)
	n.Reg.Close()
	return err
}
