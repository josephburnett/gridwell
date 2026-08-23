package cli

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/node"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/web"
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
// beyond loopback, or "" when the bind is loopback-only. Without a password
// the whole API is open; with one, the browser surface is gated but the gRPC
// node export sharing the port (federation, the shell PTY relay) is not — so
// either way a non-loopback bind should be a VPN-only address (e.g. a
// Tailscale IP), never 0.0.0.0 on an untrusted network.
func bindWarning(addr string, hasPassword bool) string {
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
	if hasPassword {
		return fmt.Sprintf(`gridwell: WARNING: listening on %s — this is NOT a loopback address.
gridwell: WARNING: the web UI requires the configured password, but the gRPC node export
gridwell: WARNING: on the same port is UNAUTHENTICATED: anyone who can reach that address
gridwell: WARNING: can read and write every tile and open live shell PTYs on this machine.
gridwell: WARNING: bind a VPN-only address (e.g. your Tailscale IP), never an open network.`, addr)
	}
	return fmt.Sprintf(`gridwell: WARNING: listening on %s — this is NOT a loopback address.
gridwell: WARNING: the API is UNAUTHENTICATED: anyone who can reach that address can read
gridwell: WARNING: and write every tile and open live shell PTYs on this machine.
gridwell: WARNING: bind a VPN-only address (e.g. your Tailscale IP), never an open network.
gridwell: WARNING: (set password: in server.yaml to at least gate the web UI.)`, addr)
}

// servingBanner is the one-line boot contract with the desktop sidecar
// (apps/desktop/src/main/lines.ts parses it): the ACTUAL bound address,
// printed only once Listen has succeeded. With a password configured it also
// carries the derived auth token (server.AuthToken — the cookie value), so
// the sidecar can authenticate its own window without ever prompting: local
// stdout is same-trust as server.yaml, which holds the password itself.
func servingBanner(addr, staticDir string, plugins int, password string) string {
	if staticDir == "" {
		staticDir = "embedded"
	}
	if password == "" {
		return fmt.Sprintf("gridwell: serving on %s (static=%s plugins=%d)", addr, staticDir, plugins)
	}
	return fmt.Sprintf("gridwell: serving on %s (static=%s plugins=%d auth=%s)",
		addr, staticDir, plugins, server.AuthToken(password))
}

// staticFS resolves the static override: "" is the embedded web client
// (the distributed-binary default, web.FS — the gridwell binary is fully
// self-contained since 2026-08-12), a path is a dev checkout on disk.
func staticFS(dir string) fs.FS {
	if dir == "" {
		return web.FS
	}
	return os.DirFS(dir)
}

// buildServeConfig is node.BuildConfig (the load/validate/inject path
// moved to the embeddable core so the CLI and the mobile bind share ONE
// serve wiring); this thin name keeps the CLI's tests and call sites.
func buildServeConfig(home, cfgPath string) (*config.ServerConfig, error) {
	return node.BuildConfig(home, cfgPath)
}

// resolveBinary finds a go-plugin binary by name (gridwell-plugin-<kind>
// or gridwell-provider-<kind>). Every plugin runs as a separately-compiled
// subprocess; the host locates the binary via GRIDWELL_PLUGIN_DIR, then
// beside the running gridwell executable (how `make` lays them out), then
// on PATH.
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

// resolvePluginBinaries fills in Binary for every plugin that didn't pin
// one explicitly in server.yaml, by kind — skipping kinds a bundled
// binary provides in-process (its factories win: that is the composer's
// whole choice; an explicit server.yaml binary: still beats both).
func resolvePluginBinaries(cfg *config.ServerConfig, factories map[string]plugin.ServerFactory) error {
	for i := range cfg.Plugins {
		pc := &cfg.Plugins[i]
		if pc.Binary != "" {
			continue
		}
		if _, ok := factories[pc.Kind]; ok {
			continue
		}
		name := "gridwell-plugin-" + pc.Kind
		if pc.Provider {
			// v2 provider entries spawn the provider binary — a distinct
			// name because a binary serves ONE service (docs/v2-design.md).
			name = "gridwell-provider-" + pc.Kind
		}
		bin, err := resolveBinary(name)
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
// RunServe runs the stock host: every plugin an out-of-process binary.
func RunServe(args []string) int { return RunServeWith(args, nil) }

// RunServeWith is RunServe for a BUNDLED binary (a leaf composer,
// docs/plugin.md): kinds present in factories load in-process through the
// same compose door; everything else spawns. The stock host passes nil.
func RunServeWith(args []string, factories map[string]plugin.ServerFactory) int {
	// The native store (the v2 fold): kind "local" is node code, always
	// in-process. A composer's own local factory (mobile) still wins.
	factories = node.WithNativeLocal(factories)
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

	// DELETE AFTER 2026-09-16 with kindmigrate.go: the one-shot kind
	// rename (localdb→local, ssh→remote).
	if err := migrateRenamedKinds(home, cfgPath); err != nil {
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

	// ONE serve per home (servelock.go): taken before any plugin spawns so a
	// second server never touches the DBs. On conflict, re-emit the running
	// holder's banner as "already serving" — the desktop app parses it and
	// connects to the existing server instead of starting its own.
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

	// Every plugin runs as a separately-compiled go-plugin subprocess. Resolve
	// each kind's binary (server.yaml may pin an explicit path instead).
	if err := resolvePluginBinaries(cfg, factories); err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		return 1
	}

	// The node core (internal/node — shared with the mobile bind): plugin
	// loading, identity, the server assembly (NodeHandler: browsers, gRPC
	// front door, and the node export on ONE port), and the listener. The
	// CLI's own concerns wrap it: the lock above, the banner below,
	// signals.
	n, err := node.Start(node.Options{
		Home:      home,
		Cfg:       cfg,
		Factories: factories,
		// The embedded web client by default — the binary is self-contained
		// (web.FS); server.yaml static:/--static is the dev override that
		// serves a checkout from disk instead.
		StaticFS: staticFS(f.StaticDir),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		return 1
	}
	defer n.Close()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	// Listen-before-announce is node.Start's contract: the "serving on"
	// banner is parsed by the desktop sidecar
	// (apps/desktop/src/main/lines.ts) to learn the origin its window
	// should load, so it must carry the listener's ACTUAL bound address
	// and appear only once the listener is really up.
	if w := bindWarning(n.Ln.Addr().String(), cfg.Password != ""); w != "" {
		fmt.Fprintln(os.Stderr, w)
	}
	banner := servingBanner(n.Ln.Addr().String(), cfg.StaticDir, len(cfg.Plugins), cfg.Password)
	fmt.Println(banner)
	// Record the banner in the lock file — the "already serving" reprint a
	// conflicting serve hands to the desktop app.
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
