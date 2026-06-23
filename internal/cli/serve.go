package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/plugin"
	fsplugin "github.com/josephburnett/gridwell/internal/plugin/fs"
	procplugin "github.com/josephburnett/gridwell/internal/plugin/proc"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/store"
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
// defaults). The DB path is local for dev convenience; production users
// supply --db or server.yaml.
var cliDefaults = config.ServerConfig{
	Bind:      "127.0.0.1:8080",
	DB:        "./gridwell.db",
	StaticDir: "./web",
}

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
	db := resolveDB(fs, fileCfg.DB)
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

// builtinFactories are the in-process plugin factories recognised by LoadAll.
var builtinFactories = map[string]plugin.ServerFactory{
	"fs": func(cfg *config.PluginConfig) (gridwellv1.GridwellServer, error) {
		return fsplugin.NewFactory(cfg)
	},
	"proc": func(cfg *config.PluginConfig) (gridwellv1.GridwellServer, error) {
		return procplugin.NewFactory(cfg)
	},
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

	s, err := store.Open(f.DB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "serve: open db: %v\n", err)
		return 1
	}
	defer s.Close()

	// Build the plugin registry from server.yaml. Non-localdb plugins (fs,
	// proc) are loaded in-process via ServeInProcess. The registry is closed
	// at shutdown, which stops their gRPC listeners. Missing or empty plugin
	// list is fine — the registry is just empty.
	var reg *plugin.Registry
	if f.cfgPath != "" {
		if fileCfg, cfgErr := config.Load(f.cfgPath); cfgErr == nil {
			if r, rErr := plugin.LoadAll(fileCfg, builtinFactories); rErr != nil {
				fmt.Fprintf(os.Stderr, "serve: load plugins: %v\n", rErr)
				return 1
			} else {
				reg = r
			}
		}
	}
	if reg == nil {
		reg = plugin.NewRegistry()
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

	srv := server.New(s, server.Config{StaticDir: f.StaticDir})
	srv.SetPluginRegistry(reg)
	if uuid, uErr := s.PluginUUID(context.Background()); uErr == nil && uuid != "" {
		srv.SetLocaldbUUID(uuid)
	}
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
