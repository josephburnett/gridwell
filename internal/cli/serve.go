package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
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
	"github.com/josephburnett/gridwell/internal/tmux"
)

// serveFlags holds the parsed `serve` subcommand options. Split out from
// RunServe so the flag-parsing path is unit-testable.
type serveFlags struct {
	DB        string
	Bind      string
	StaticDir string
	// cfgPath is the resolved server.yaml path used to load plugin config.
	cfgPath string
}

// cliDefaults are the flag-level defaults: what you get with no config file
// and no flags. Distinct from config.Defaults (which describe server.yaml
// defaults).
var cliDefaults = config.ServerConfig{
	Bind:      "127.0.0.1:8080",
	StaticDir: "./web",
}

// cliDefaultDB is the --db default: the db_file for the synthesized root
// localdb plugin when no server.yaml designates one. Local for dev
// convenience; production users supply --db or a server.yaml root plugin.
const cliDefaultDB = "./gridwell.db"

// defaultRootID is the plugin id assigned to the synthesized root localdb when
// no server.yaml is present. Stable so ids stored against it keep resolving.
const defaultRootID = "gridwell-root"

// parseServeFlags parses the `serve` flag set. Returns the populated
// struct, or an error if flag parsing fails (so the caller can decide
// the exit code).
//
// Precedence: CLI flags > server.yaml at cfgPath > cliDefaults.
// cfgPath="" disables config file loading (used by tests).
func parseServeFlags(args []string, cfgPath string) (serveFlags, error) {
	// Load config file for defaults (missing file is fine).
	fileCfg := cliDefaults
	if cfgPath != "" {
		if fc, err := config.Load(cfgPath); err == nil {
			fileCfg = *fc
		}
	}

	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	var f serveFlags
	f.cfgPath = cfgPath
	db := resolveDB(fs, cliDefaultDB)
	fs.StringVar(&f.Bind, "bind", fileCfg.Bind, "HTTP listen address")
	fs.StringVar(&f.StaticDir, "static", fileCfg.StaticDir, "directory of static files served at / (empty = headless)")
	args = reorderFlagsFirst(args, func(name string) bool {
		switch name {
		case "db", "bind", "static":
			return true
		}
		return false
	})
	if err := fs.Parse(args); err != nil {
		return serveFlags{}, err
	}
	f.DB = *db
	return f, nil
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

// RunServe starts the backend HTTP server — the loopback data plane for the
// Gridwell desktop app: Connect-RPC, the SSE event stream, the wasm client,
// and shell PTYs. Live URL tiles are hosted natively by the Electron shell,
// so there is no browser driver here. SIGINT/SIGTERM trigger graceful
// shutdown.
func RunServe(args []string) int {
	cfgPath, err := config.DefaultPath()
	if err != nil {
		cfgPath = ""
	}
	f, err := parseServeFlags(args, cfgPath)
	if err != nil {
		return 2
	}

	// Build the effective config: server.yaml lists every plugin. With no
	// config we synthesize a single localdb plugin backed by --db — a plugin
	// like any other (the client enters it from the launcher), not a root.
	cfg := &config.ServerConfig{Bind: f.Bind, StaticDir: f.StaticDir}
	if f.cfgPath != "" {
		if loaded, cfgErr := config.Load(f.cfgPath); cfgErr == nil {
			cfg = loaded
		}
	}
	if len(cfg.Plugins) == 0 {
		cfg.Plugins = []config.PluginConfig{{
			ID:     defaultRootID,
			Name:   "local",
			Kind:   "localdb",
			Config: map[string]string{"db_file": f.DB},
		}}
	}

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

	// The gridwell-private tmux server backs every shell tile. One
	// socket per gridwell process; sessions named `gridwell-<tileID>`
	// survive ascents and gridwell restarts (bash + scrollback live
	// in tmux). Reboots take everything with them; the snapshot
	// remains and the wasm hides the refresh button.
	tmuxCtrl, tmuxCleanup, err := tmux.New("gridwell", "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "serve: tmux init: %v\n", err)
		return 1
	}
	defer func() { _ = tmuxCleanup() }()

	srv := server.New(reg, server.Config{StaticDir: f.StaticDir})
	srv.SetShellStreamer(server.NewLiveShellStreamer(tmuxCtrl))

	// Bound the orphan leak: any tmux session whose tile id no longer
	// exists is left over from a delete that raced a previous crash.
	if killed, err := srv.CleanupOrphanedShellSessions(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "gridwell: orphan cleanup: %v\n", err)
	} else if killed > 0 {
		fmt.Printf("gridwell: orphan cleanup killed %d stale shell session(s)\n", killed)
	}

	requestCtx, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()

	httpSrv := &http.Server{
		Addr:              f.Bind,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return requestCtx },
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	errCh := make(chan error, 1)
	go func() {
		fmt.Printf("gridwell: serving on %s (db=%s static=%s)\n", f.Bind, f.DB, f.StaticDir)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
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
