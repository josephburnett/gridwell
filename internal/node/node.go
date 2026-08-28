// Package node is the EMBEDDABLE Gridwell node: everything `gridwell
// serve` does between "config in hand" and "listener up", as a library —
// plugin loading (subprocess or in-process), node identity, the server
// assembly, and the lifecycle. Extracted (offline-plan phase 2) so the
// CLI and the mobile bind (mobile/) share ONE serve wiring instead of
// drifting copies: the CLI adds flags, the serve lock, the banner and
// signal handling around it; mobile adds its bundled provider factories
// and auto-init. Neither reimplements the middle.
package node

import (
	"encoding/json"

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
	"github.com/josephburnett/gridwell/internal/remote"
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
	// The web door is never open (owner decision 2026-08-26): the password
	// is the web-password file beside the config, minted here on first
	// serve, printed by serve, rotated by deleting it.
	if cfg.WebPassword, err = config.EnsurePasswordFile(home); err != nil {
		return nil, err
	}
	if cfg.Federation.Socket == "" {
		cfg.Federation.Socket = config.FederationSocket(home)
	}
	if err := injectConnections(cfg); err != nil {
		return nil, err
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
		// A NATIVE kind's DB must already exist: it is created once by
		// init. serve never creates one — otherwise a changed id (whose
		// derived path doesn't exist) would silently spawn a fresh, empty
		// store instead of failing. A PROVIDER's derived path is the
		// NODE-owned memory DB (docs/v2-design.md §3.2), durable-but-
		// FORGETTABLE by contract — creating it empty is the defined
		// recovery from losing it, so serve creates it freely
		// (layout.OpenVerified stamps identity at creation).
		if !IsNative(pc.Kind) {
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

// nativeFactories are the kinds the NODE itself implements over
// gridwell.v1 — the local store and the remote transport — the only
// kinds that are not content providers (docs/content-presentation.md
// §9). The ONE owner of that distinction and of their construction:
// init creates a DB for exactly these, serve spawns nothing for them,
// Start constructs them here — no leaf (desktop, mobile) composes its
// own copy (2026-08-27: mobile's copy had drifted — its remote factory
// skipped the connections: config mode, so a phone ignored the yaml).
var nativeFactories = map[string]plugin.NativeFactory{
	"local":  NativeLocalFactory,
	"remote": NativeRemoteFactory,
}

// IsNative reports whether kind is one of the node's own kinds.
func IsNative(kind string) bool {
	_, ok := nativeFactories[kind]
	return ok
}

// Init registers one entry in a home: mint the durable id, create a
// NATIVE kind's DB with its identity stamped (pluginmeta) — a provider
// gets no DB here; its node-owned memory DB is minted at first serve —
// append the server.yaml entry, and ensure the node's own id. The one
// init door: the CLI's `gridwell init` and mobile's first-run auto-init
// both come through here, so a home initialized on any platform is
// byte-compatible with every other.
func Init(home, kind, name string, conf map[string]string) (id string, err error) {
	id = idshape.NewShortID()
	dbDir := config.DBDir(home, id)
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		return "", err
	}
	if IsNative(kind) {
		if err := pluginmeta.Create(config.DBFile(home, id), id, kind); err != nil {
			return "", err
		}
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
	// first entry so a fresh home is fully identified before first serve.
	if _, err := config.EnsureNodeID(home, idshape.NewShortID); err != nil {
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
	// Factories, when non-nil, provides in-process constructors
	// for provider entries whose Binary is empty (bundled binaries;
	// mobile — iOS forbids fork/exec; tests). The native kinds need no
	// such door: the node constructs them itself.
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
}

// Start assembles the node — plugins, identity, server — and LISTENS,
// but does not serve yet: the caller announces the bound address first
// (the CLI's banner contract) and then calls ServeBackground. On error
// nothing is left running.
func Start(opts Options) (*Node, error) {
	cfg := opts.Cfg
	reg, err := plugin.LoadAll(cfg, nativeFactories, opts.Factories)
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
		NodeStatePath: config.NodeViewFile(opts.Home),
		Password:      cfg.WebPassword,
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

// injectConnections carries server.yaml's connections: declarations to
// the builtin transport through the one flat config vocabulary (v2
// #269). It lives in BuildConfig — NODE code — so every leaf that builds
// a config gets it (2026-08-27: it was serve-only, and the mobile node
// silently ignored the connections: key). Exactly one remote entry may exist when the key is present —
// two transports sharing one connection list would double-materialize.
func injectConnections(cfg *config.ServerConfig) error {
	conns, set := cfg.ConnectionList()
	if !set {
		return nil
	}
	var remotes []*config.PluginConfig
	for i := range cfg.Plugins {
		if cfg.Plugins[i].Kind == "remote" {
			remotes = append(remotes, &cfg.Plugins[i])
		}
	}
	if len(remotes) == 0 {
		if len(conns) > 0 {
			return fmt.Errorf("connections: declared but no remote transport entry exists — `gridwell init --kind remote --name far` first")
		}
		return nil
	}
	if len(remotes) > 1 {
		return fmt.Errorf("connections: %d remote entries — one transport owns the connection list; remove the extras", len(remotes))
	}
	// The TYPED spec, not a hand-keyed map: remote.ConnSpec is the shape
	// nativeremote unmarshals, so marshaling it here is the one place the
	// yaml vocabulary meets the transport's. TestInjectConnectionsCarries
	// EveryField pins the mapping exhaustive — a field added to ConnSpec
	// without a line here fails that test instead of silently dropping.
	specs := make([]remote.ConnSpec, 0, len(conns))
	for _, c := range conns {
		specs = append(specs, remote.ConnSpec{
			Name: c.Name, Label: c.Label, Host: c.Host, User: c.User,
			Port: c.Port, Addr: c.Addr, Key: c.Key, KnownHosts: c.KnownHosts,
		})
	}
	blob, err := json.Marshal(specs)
	if err != nil {
		return err
	}
	pc := remotes[0]
	if pc.Config == nil {
		pc.Config = map[string]string{}
	}
	pc.Config["connections_json"] = string(blob)
	if len(cfg.RetiredNames) > 0 {
		r, err := json.Marshal(cfg.RetiredNames)
		if err != nil {
			return err
		}
		pc.Config["retired_json"] = string(r)
	}
	return nil
}
