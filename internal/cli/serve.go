package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/store"
)

// serveFlags holds the parsed `serve` subcommand options. Split out from
// RunServe so the flag-parsing path is unit-testable. The DB path is no longer
// a flag — it is derived from each plugin's id under the Gridwell home.
type serveFlags struct {
	Bind        string
	BindDefault string
	StaticDir   string
}

// parseServeFlags parses the `serve` flag set. StaticDir defaults to defStatic
// (the server.yaml value, already default-filled by config.Load). Bind and
// BindDefault deliberately default to empty: "" means "not passed", which is
// what resolveBind needs to apply its precedence — the bind decision is made
// there, not by flag defaulting.
func parseServeFlags(args []string, defStatic string) (serveFlags, error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	var f serveFlags
	fs.StringVar(&f.Bind, "bind", "", "HTTP listen address (hard override: beats server.yaml bind:)")
	fs.StringVar(&f.BindDefault, "bind-default", "", "HTTP listen address used only when server.yaml has no bind: (the desktop sidecar passes its ephemeral loopback port here)")
	fs.StringVar(&f.StaticDir, "static", defStatic, "directory of static files served at / (empty = headless)")
	args = reorderFlagsFirst(args, func(name string) bool {
		switch name {
		case "bind", "bind-default", "static":
			return true
		}
		return false
	})
	if err := fs.Parse(args); err != nil {
		return serveFlags{}, err
	}
	return f, nil
}

// resolveBind is the one owner of the listen-address decision:
//
//	--bind (a human's hard override)
//	> server.yaml bind: (explicitly present — configBindSet, see config.Load)
//	> --bind-default (the caller's fallback, e.g. the desktop sidecar's
//	  ephemeral loopback port)
//	> the built-in default (config.Defaults.Bind).
//
// "Unset" is the empty string at every level, so an explicit config bind equal
// to the built-in default still pins the address. This is what lets one server
// instance carry both the desktop window and a phone: declare bind: in
// server.yaml and the sidecar's --bind-default no longer wins.
func resolveBind(flagBind, configBind string, configBindSet bool, bindDefault string) string {
	switch {
	case flagBind != "":
		return flagBind
	case configBindSet:
		return configBind
	case bindDefault != "":
		return bindDefault
	default:
		return config.Defaults.Bind
	}
}

// bindWarning returns a prominent startup warning when addr exposes the server
// beyond loopback, or "" when the bind is loopback-only. Gridwell's API has no
// authentication: anyone who can reach the port can read and write every tile,
// open live shell PTYs on this machine, and copy plugin session blobs — so a
// non-loopback bind should be a VPN-only address (e.g. a Tailscale IP), never
// 0.0.0.0 on an untrusted network.
func bindWarning(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr // no port part — judge the host as given
	}
	if host == "localhost" {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return ""
	}
	return fmt.Sprintf(`gridwell: WARNING: listening on %s — this is NOT a loopback address.
gridwell: WARNING: the API is UNAUTHENTICATED: anyone who can reach that address can read
gridwell: WARNING: and write every tile and open live shell PTYs on this machine.
gridwell: WARNING: bind a VPN-only address (e.g. your Tailscale IP), never an open network.`, addr)
}

// buildServeConfig loads the mandatory server.yaml at cfgPath and prepares it
// for launch: the file must exist and list at least one plugin (no synthesized
// fallback), and every plugin gets its derived db_file injected (the path is
// never stored in the config — it is fixed at <home>/db/<id>/store.db). Split
// out from RunServe so the load/validate/inject path is unit-testable.
func buildServeConfig(home, cfgPath string) (*config.ServerConfig, error) {
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
	for i := range cfg.Plugins {
		pc := &cfg.Plugins[i]
		if pc.Config == nil {
			pc.Config = map[string]string{}
		}
		dbFile := config.DBFile(home, pc.ID)
		pc.Config["db_file"] = dbFile
		// The DB must already exist: it is created once by `gridwell init`. serve
		// never creates one — otherwise a changed id (whose derived path doesn't
		// exist) would silently spawn a fresh, empty store instead of failing.
		if _, err := os.Stat(dbFile); err != nil {
			return nil, fmt.Errorf("plugin %q (%s): no database at %s; run `gridwell init` to create it", pc.Name, pc.ID, dbFile)
		}
	}
	return cfg, nil
}

// resolvePluginBinary finds the go-plugin binary for a built-in kind:
// "gridwell-<kind>". Every plugin runs as a separately-compiled subprocess;
// the host locates the binary via GRIDWELL_PLUGIN_DIR, then beside the running
// gridwell executable (how `make` lays them out), then on PATH.
func resolvePluginBinary(kind string) (string, error) {
	name := "gridwell-" + kind
	var tried []string
	if dir := os.Getenv("GRIDWELL_PLUGIN_DIR"); dir != "" {
		p := filepath.Join(dir, name)
		if isExecutable(p) {
			return p, nil
		}
		tried = append(tried, p)
	}
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), name)
		if isExecutable(p) {
			return p, nil
		}
		tried = append(tried, p)
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("plugin binary %q not found (tried %v, then PATH); set GRIDWELL_PLUGIN_DIR or run `make plugins`", name, tried)
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

// resolvePluginBinaries fills in Binary for every plugin that didn't pin one
// explicitly in server.yaml, by kind. Production always runs subprocess plugins.
func resolvePluginBinaries(cfg *config.ServerConfig) error {
	for i := range cfg.Plugins {
		pc := &cfg.Plugins[i]
		if pc.Binary != "" {
			continue
		}
		bin, err := resolvePluginBinary(pc.Kind)
		if err != nil {
			return fmt.Errorf("plugin %q (%s): %w", pc.Name, pc.Kind, err)
		}
		pc.Binary = bin
	}
	return nil
}

// RunServe starts the backend HTTP server — the data plane for the Gridwell
// desktop app and any plain-browser client: Connect-RPC, the SSE event
// stream, the wasm client, and shell PTYs. Live URL tiles are hosted natively
// by the Electron shell, so there is no browser driver here. The listen
// address comes from resolveBind (loopback by default; server.yaml bind: pins
// it, e.g. to a Tailscale IP for phone access). SIGINT/SIGTERM trigger
// graceful shutdown.
func RunServe(args []string) int {
	home, err := config.Home()
	if err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		return 1
	}
	cfgPath, err := config.DefaultPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		return 1
	}

	// The config is mandatory and authoritative: it lists every plugin and the
	// id+kind the server verifies against each DB. No synthesized fallback.
	cfg, err := buildServeConfig(home, cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		return 1
	}

	f, err := parseServeFlags(args, cfg.StaticDir)
	if err != nil {
		return 2
	}
	cfg.Bind = resolveBind(f.Bind, cfg.Bind, cfg.BindSet, f.BindDefault)
	cfg.StaticDir = f.StaticDir

	// Every plugin runs as a separately-compiled go-plugin subprocess. Resolve
	// each kind's binary (server.yaml may pin an explicit path instead).
	if err := resolvePluginBinaries(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		return 1
	}

	reg, err := plugin.LoadAll(cfg, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "serve: load plugins: %v\n", err)
		return 1
	}
	defer reg.Close()

	// The node's own durable identity qualifies the node grid (the plugin-list
	// landing page). Minted once and persisted into server.yaml; a pre-node
	// config gains the field on first serve without touching any plugin id.
	nodeID, err := config.EnsureNodeID(home, store.NewUUID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "serve: node id: %v\n", err)
		return 1
	}

	// Shell PTYs now live in the owning plugin (OpenShell); the server is a pure
	// bridge. tmux, the session lifecycle, and orphan cleanup all moved behind
	// the interface — the localdb plugin binary owns them.
	srv := server.New(reg, server.Config{
		StaticDir: f.StaticDir,
		NodeID:    nodeID,
		// The landing page's viewport survives restarts in a small state
		// file beside the config ("things stay as you left them").
		NodeStatePath: filepath.Join(home, "node-view.json"),
	})

	requestCtx, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()

	// NodeHandler = the browser mux wrapped in h2c plus the per-plugin gRPC
	// node export, so this one port serves every caller: browsers (HTTP/1.1),
	// gRPC front-door calls, and a remote mounter's plugin-scoped gRPC (the
	// ssh plugin through its tunnel). See internal/server/nodeexport.go.
	httpSrv := &http.Server{
		Handler:           srv.NodeHandler(),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return requestCtx },
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	// Listen before announcing: the "serving on" banner is a contract — the
	// desktop sidecar parses the address out of this exact line
	// (apps/desktop/src/main/lines.ts) to learn the origin its window should
	// load, so it must carry the listener's ACTUAL bound address and appear
	// only once the listener is really up. The server, not its spawner, owns
	// the "where am I listening" fact.
	ln, err := net.Listen("tcp", cfg.Bind)
	if err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		return 1
	}
	if w := bindWarning(ln.Addr().String()); w != "" {
		fmt.Fprintln(os.Stderr, w)
	}
	fmt.Printf("gridwell: serving on %s (static=%s plugins=%d)\n", ln.Addr(), cfg.StaticDir, len(cfg.Plugins))

	errCh := make(chan error, 1)
	go func() {
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-stop:
		fmt.Println("gridwell: shutting down")
	case err := <-errCh:
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		return 1
	}
	cancelRequests()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "shutdown: %v\n", err)
		return 1
	}
	return 0
}
