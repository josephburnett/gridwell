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
)

// serveFlags holds the parsed `serve` subcommand options. Split out from
// RunServe so the flag-parsing path is unit-testable. The DB path is no longer
// a flag — it is derived from each plugin's id under the Gridwell home.
type serveFlags struct {
	Bind      string
	StaticDir string
}

// parseServeFlags parses the `serve` flag set, using defBind/defStatic (the
// server.yaml values, already default-filled by config.Load) as the flag
// defaults so CLI flags override the config which overrides the built-ins.
func parseServeFlags(args []string, defBind, defStatic string) (serveFlags, error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	var f serveFlags
	fs.StringVar(&f.Bind, "bind", defBind, "HTTP listen address")
	fs.StringVar(&f.StaticDir, "static", defStatic, "directory of static files served at / (empty = headless)")
	args = reorderFlagsFirst(args, func(name string) bool {
		switch name {
		case "bind", "static":
			return true
		}
		return false
	})
	if err := fs.Parse(args); err != nil {
		return serveFlags{}, err
	}
	return f, nil
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
		pc.Config["db_file"] = config.DBFile(home, pc.ID)
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

// RunServe starts the backend HTTP server — the loopback data plane for the
// Gridwell desktop app: Connect-RPC, the SSE event stream, the wasm client,
// and shell PTYs. Live URL tiles are hosted natively by the Electron shell,
// so there is no browser driver here. SIGINT/SIGTERM trigger graceful
// shutdown.
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

	f, err := parseServeFlags(args, cfg.Bind, cfg.StaticDir)
	if err != nil {
		return 2
	}
	cfg.Bind, cfg.StaticDir = f.Bind, f.StaticDir

	// Each plugin's DB lives at <home>/db/<id>/store.db; ensure the directory
	// exists before the plugin opens (the plugin creates the file, not the dir).
	for i := range cfg.Plugins {
		if err := os.MkdirAll(config.DBDir(home, cfg.Plugins[i].ID), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "serve: %v\n", err)
			return 1
		}
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

	// Shell PTYs now live in the owning plugin (OpenShell); the server is a pure
	// bridge. tmux, the session lifecycle, and orphan cleanup all moved behind
	// the interface — the localdb plugin binary owns them.
	srv := server.New(reg, server.Config{StaticDir: f.StaticDir})

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
		fmt.Printf("gridwell: serving on %s (static=%s plugins=%d)\n", cfg.Bind, cfg.StaticDir, len(cfg.Plugins))
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
