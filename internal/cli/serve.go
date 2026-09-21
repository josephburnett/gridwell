package cli

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/node"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/web"
)

// serveFlags is the parsed `serve` options, split from RunServe so flag
// parsing is unit-testable. The database path is derived from the home.
type serveFlags struct {
	Bind        string
	BindDefault string
	StaticDir   string
}

// parseServeFlags parses the `serve` flag set. Bind and BindDefault default
// to empty because "" means not passed, which is the precedence resolveBind
// needs; the bind decision is made there, not by flag defaulting.
func parseServeFlags(args []string, defStatic string) (serveFlags, error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	var f serveFlags
	fs.StringVar(&f.Bind, "bind", "", "web listen address (hard override: beats server.yaml web.bind)")
	fs.StringVar(&f.BindDefault, "bind-default", "", "web listen address used only when server.yaml has no web.bind (the desktop sidecar passes its ephemeral loopback port here)")
	fs.StringVar(&f.StaticDir, "static", defStatic, "serve static files from this directory instead of the embedded web client (dev override; empty = embedded)")
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

// resolveBind is the one owner of the web listen-address decision:
//
//	--bind > server.yaml web.bind (present; BindSet) > --bind-default >
//	config.Defaults.Web.Bind
//
// Unset is "" at every level, so an explicit config bind equal to the
// built-in default still pins the address. That is what lets one server
// carry both the desktop window and a phone: declare web.bind and the
// sidecar's --bind-default no longer wins.
func resolveBind(flagBind, configBind string, configBindSet bool, bindDefault string) string {
	switch {
	case flagBind != "":
		return flagBind
	case configBindSet:
		return configBind
	case bindDefault != "":
		return bindDefault
	default:
		return config.Defaults.Web.Bind
	}
}

// bannerPrefix opens servingBanner's line; the sidecar and the "already
// serving" reprint match on it.
const bannerPrefix = "gridwell: serving on "

// servingBanner is the one-line boot contract with the desktop sidecar,
// parsed by apps/desktop/src/main/lines.ts. The web door's bound address
// leads. auth= is the cookie value, so the sidecar authenticates its own
// window without prompting; local stdout is the same trust level as the
// web-password file. federation= is last and runs to the closing paren,
// because a socket path may contain spaces.
func servingBanner(addr, fedSocket, staticDir string, plugins int, password string) string {
	if staticDir == "" {
		staticDir = "embedded"
	}
	return fmt.Sprintf(bannerPrefix+"%s (static=%s plugins=%d auth=%s federation=%s)",
		addr, staticDir, plugins, server.AuthToken(password), fedSocket)
}

// staticFS resolves the static override: "" is the embedded web client, so
// the gridwell binary is self-contained.
func staticFS(dir string) fs.FS {
	if dir == "" {
		return web.FS
	}
	return os.DirFS(dir)
}

// buildServeConfig is node.BuildConfig under the name the CLI's tests and
// call sites use.
func buildServeConfig(home, cfgPath string) (*config.ServerConfig, error) {
	return node.BuildConfig(home, cfgPath)
}

// exeSuffixFor and execBitRequiredOn are pure functions of GOOS so the
// Windows facts are testable from a host nobody runs the suite on. The
// suffix is also what the Makefile's plugins target lays out, so the loader
// and the build agree without consulting each other.
func exeSuffixFor(goos string) string {
	if goos == "windows" {
		return ".exe"
	}
	return ""
}

// Windows has no execute bit, so the .exe extension is the whole fact there;
// testing the unix bits would reject every plugin binary that exists.
func execBitRequiredOn(goos string) bool { return goos != "windows" }

// resolveBinary finds gridwell-plugin-<kind> through GRIDWELL_PLUGIN_DIR,
// then beside the running executable, which is how make lays them out, then
// on PATH.
func resolveBinary(name string) (string, error) {
	name += exeSuffixFor(runtime.GOOS)
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
	if err != nil || info.IsDir() {
		return false
	}
	if !execBitRequiredOn(runtime.GOOS) {
		return true
	}
	return info.Mode()&0o111 != 0
}

// resolvePluginBinaries fills each entry's binary: every kind spawns
// gridwell-plugin-<kind> unless server.yaml pins a path.
func resolvePluginBinaries(cfg *config.ServerConfig) error {
	for i := range cfg.Plugins {
		pc := &cfg.Plugins[i]
		if pc.Binary != "" {
			continue
		}
		bin, err := resolveBinary("gridwell-plugin-" + pc.Kind)
		if err != nil {
			return fmt.Errorf("plugin %q (%s): %w", pc.ID, pc.Kind, err)
		}
		pc.Binary = bin
	}
	return nil
}

// RunServe starts the backend HTTP server: Connect-RPC, the event stream,
// the wasm client, and shell PTYs. Live url tiles are hosted natively by the
// Electron shell, so there is no browser driver here. The listen address
// comes from resolveBind. SIGINT and SIGTERM shut down gracefully.
func RunServe(args []string) int {
	home, err := config.Home()
	if err != nil {
		return die("serve", err)
	}
	cfgPath, err := config.DefaultPath()
	if err != nil {
		return die("serve", err)
	}

	// A missing config file is a fresh home; the node mints its id and
	// writes the file.
	cfg, err := buildServeConfig(home, cfgPath)
	if err != nil {
		return die("serve", err)
	}

	f, err := parseServeFlags(args, cfg.StaticDir)
	if err != nil {
		return 2
	}
	cfg.Web.Bind = resolveBind(f.Bind, cfg.Web.Bind, cfg.Web.BindSet, f.BindDefault)
	cfg.StaticDir = f.StaticDir

	// One serve per home, see servelock.go, taken before any plugin spawns.
	// On conflict re-emit the holder's banner as "already serving": the
	// desktop app parses it and connects to the existing server.
	lock, err := acquireServeLock(home)
	if err != nil {
		var held *errServeLockHeld
		if errors.As(err, &held) && strings.HasPrefix(held.banner, bannerPrefix) {
			fmt.Println("gridwell: already " + strings.TrimPrefix(held.banner, "gridwell: "))
		}
		return die("serve", err)
	}
	defer lock.Release()

	if err := resolvePluginBinaries(cfg); err != nil {
		return die("serve", err)
	}

	// The CLI's concerns wrap node.Start: the lock above, the banner below,
	// signals.
	n, err := node.Start(node.Options{
		Home: home,
		Cfg:  cfg,
		// server.yaml static: and --static serve a checkout from disk.
		StaticFS: staticFS(f.StaticDir),
	})
	if err != nil {
		return die("serve", err)
	}
	defer n.Close()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	// Listen-before-announce is node.Start's contract: the sidecar learns the
	// origin its window loads from this banner, so it must carry the real
	// bound address and appear only once the listener is up.
	banner := servingBanner(n.Ln.Addr().String(), cfg.Federation.Socket, cfg.StaticDir, len(cfg.Plugins), cfg.WebPassword)
	fmt.Println(banner)
	// The password itself, for the human at the process.
	fmt.Fprintf(os.Stderr, "gridwell: web password: %s  (%s — delete the file to rotate; every browser logs in again)\n",
		cfg.WebPassword, config.PasswordFile(home))
	// The lock file's banner is the "already serving" reprint.
	lock.WriteBanner(banner)

	errCh := n.ServeBackground()
	select {
	case <-stop:
		fmt.Println("gridwell: shutting down")
	case err := <-errCh:
		return die("serve", err)
	}
	if err := n.Close(); err != nil {
		return die("shutdown", err)
	}
	return 0
}
