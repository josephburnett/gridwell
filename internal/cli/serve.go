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
	"strings"
	"syscall"

	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/node"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/web"
)

// serveFlags holds the parsed `serve` subcommand options. It is split out
// from RunServe so the flag-parsing path is unit-testable. The database
// path is not a flag; it is derived from the Gridwell home.
type serveFlags struct {
	Bind        string
	BindDefault string
	StaticDir   string
}

// parseServeFlags parses the `serve` flag set. StaticDir defaults to
// defStatic, the server.yaml value config.Load already default-filled. Bind
// and BindDefault deliberately default to empty: "" means not passed, which
// is what resolveBind needs to apply its precedence. The bind decision is
// made there, not by flag defaulting.
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
//	--bind (a human's hard override)
//	> server.yaml web.bind (explicitly present; BindSet, see config.Load)
//	> --bind-default (the caller's fallback, such as the desktop sidecar's
//	  ephemeral loopback port)
//	> the built-in default (config.Defaults.Web.Bind).
//
// Unset is the empty string at every level, so an explicit config bind
// equal to the built-in default still pins the address. That is what lets
// one server instance carry both the desktop window and a phone: declare
// web.bind in server.yaml and the sidecar's --bind-default no longer wins.
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

// servingBanner is the one-line boot contract with the desktop sidecar,
// which apps/desktop/src/main/lines.ts parses. The web door's actual bound
// address leads, printed only once both listeners are up. auth= is the
// derived auth token (server.AuthToken, the cookie value; a password is
// always configured), so the sidecar can authenticate its own window
// without prompting; local stdout is the same trust level as the
// <home>/web-password file the password is read from. federation= is last
// and runs to the closing paren: the node door's unix socket path, which
// may contain spaces. It is how an operator sees which socket a mounter
// must reach.
func servingBanner(addr, fedSocket, staticDir string, plugins int, password string) string {
	if staticDir == "" {
		staticDir = "embedded"
	}
	return fmt.Sprintf("gridwell: serving on %s (static=%s plugins=%d auth=%s federation=%s)",
		addr, staticDir, plugins, server.AuthToken(password), fedSocket)
}

// staticFS resolves the static override: "" is the embedded web client
// (web.FS; the gridwell binary is self-contained), and a path serves a
// checkout from disk instead.
func staticFS(dir string) fs.FS {
	if dir == "" {
		return web.FS
	}
	return os.DirFS(dir)
}

// buildServeConfig is node.BuildConfig, the load, validate, and inject
// path. It lives in the embeddable core, with the rest of the serve wiring;
// this name keeps the CLI's tests and call sites.
func buildServeConfig(home, cfgPath string) (*config.ServerConfig, error) {
	return node.BuildConfig(home, cfgPath)
}

// resolveBinary finds a plugin binary, gridwell-plugin-<kind>: through
// GRIDWELL_PLUGIN_DIR, then beside the running gridwell executable, which
// is how make lays them out, then on PATH.
func resolveBinary(name string) (string, error) {
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

// resolvePluginBinaries fills each entry's binary: every kind spawns
// gridwell-plugin-<kind>. server.yaml may pin an explicit binary: path
// instead.
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

// RunServe starts the backend HTTP server: the data plane for the
// desktop app and any plain-browser client, carrying Connect-RPC, the event
// stream, the wasm client, and shell PTYs. Live url tiles are hosted
// natively by the Electron shell, so there is no browser driver here. The
// listen address comes from resolveBind: loopback by default, and
// server.yaml web.bind pins it, for instance to a Tailscale address for
// phone access. SIGINT and SIGTERM trigger graceful shutdown.
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

	// The config is authoritative: it names the node's id, its connections,
	// and its content plugins. A missing file is a fresh home, and the node
	// mints its id and writes the file.
	cfg, err := buildServeConfig(home, cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		return 1
	}

	f, err := parseServeFlags(args, cfg.StaticDir)
	if err != nil {
		return 2
	}
	cfg.Web.Bind = resolveBind(f.Bind, cfg.Web.Bind, cfg.Web.BindSet, f.BindDefault)
	cfg.StaticDir = f.StaticDir

	// One serve per home, see servelock.go. The lock is taken before any
	// plugin spawns, so a second server never touches the database. On
	// conflict, re-emit the running holder's banner as "already serving":
	// the desktop app parses it and connects to the existing server instead
	// of starting its own.
	lock, err := acquireServeLock(home)
	if err != nil {
		var held *errServeLockHeld
		if errors.As(err, &held) && strings.HasPrefix(held.banner, "gridwell: serving on ") {
			fmt.Println("gridwell: already " + strings.TrimPrefix(held.banner, "gridwell: "))
		}
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		return 1
	}
	defer lock.Release()

	// Resolve each plugin's binary; server.yaml may pin an explicit path
	// instead.
	if err := resolvePluginBinaries(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		return 1
	}

	// The node core, internal/node: plugin loading, identity, the server
	// assembly, and the two listeners — the web door where config says, and
	// the connection door's unix socket. The CLI's own concerns wrap it: the
	// lock above, the banner below, signals.
	n, err := node.Start(node.Options{
		Home: home,
		Cfg:  cfg,
		// The embedded web client by default; the binary is self-contained.
		// server.yaml static: and --static serve a checkout from disk
		// instead.
		StaticFS: staticFS(f.StaticDir),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		return 1
	}
	defer n.Close()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	// Listen-before-announce is node.Start's contract. The desktop sidecar
	// parses the "serving on" banner to learn the origin its window should
	// load, so the banner must carry the listener's actual bound address and
	// appear only once the listener is really up.
	banner := servingBanner(n.Ln.Addr().String(), cfg.Federation.Socket, cfg.StaticDir, len(cfg.Plugins), cfg.WebPassword)
	fmt.Println(banner)
	// The password itself, for the human at the process: carry it to a
	// browser once and the cookie lasts.
	fmt.Fprintf(os.Stderr, "gridwell: web password: %s  (%s — delete the file to rotate; every browser logs in again)\n",
		cfg.WebPassword, config.PasswordFile(home))
	// Record the banner in the lock file: it is the "already serving"
	// reprint a conflicting serve hands to the desktop app.
	lock.WriteBanner(banner)

	errCh := n.ServeBackground()
	select {
	case <-stop:
		fmt.Println("gridwell: shutting down")
	case err := <-errCh:
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		return 1
	}
	if err := n.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "shutdown: %v\n", err)
		return 1
	}
	return 0
}
