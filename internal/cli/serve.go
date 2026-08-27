package cli

import (
	"encoding/json"
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
	"github.com/josephburnett/gridwell/internal/remote"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/web"
)

// serveFlags holds the parsed `serve` subcommand options. Split out from
// RunServe so the flag-parsing path is unit-testable. The DB path is no longer
// a flag — it is derived from each plugin's id under the Gridwell home.
type serveFlags struct {
	Bind        string
	BindDefault string
	// FederationPort / FederationPortDefault mirror Bind / BindDefault for
	// the node door: -1 = not passed (0 is a real value: an ephemeral port).
	FederationPort        int
	FederationPortDefault int
	StaticDir             string
}

// parseServeFlags parses the `serve` flag set. StaticDir defaults to defStatic
// (the server.yaml value, already default-filled by config.Load). Bind and
// BindDefault deliberately default to empty: "" means "not passed", which is
// what resolveBind needs to apply its precedence — the bind decision is made
// there, not by flag defaulting.
func parseServeFlags(args []string, defStatic string) (serveFlags, error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	var f serveFlags
	fs.StringVar(&f.Bind, "bind", "", "web listen address (hard override: beats server.yaml web.bind)")
	fs.StringVar(&f.BindDefault, "bind-default", "", "web listen address used only when server.yaml has no web.bind (the desktop sidecar passes its ephemeral loopback port here)")
	fs.IntVar(&f.FederationPort, "federation-port", -1, "loopback port for the node export (hard override: beats server.yaml federation.port; 0 = ephemeral)")
	fs.IntVar(&f.FederationPortDefault, "federation-port-default", -1, "node export port used only when server.yaml has no federation.port (the desktop sidecar passes 0)")
	fs.StringVar(&f.StaticDir, "static", defStatic, "serve static files from this directory instead of the embedded web client (dev override; empty = embedded)")
	args = reorderFlagsFirst(args, func(name string) bool {
		switch name {
		case "bind", "bind-default", "federation-port", "federation-port-default", "static":
			return true
		}
		return false
	})
	if err := fs.Parse(args); err != nil {
		return serveFlags{}, err
	}
	return f, nil
}

// resolveSetting is the one owner of the listener-setting precedence,
// for both doors:
//
//	the flag (a human's hard override)
//	> server.yaml (explicitly present — the Load-derived *Set bit)
//	> the flag's -default twin (the caller's fallback, e.g. the desktop
//	  sidecar's ephemeral loopback port)
//	> the built-in default.
//
// "Unset" is the given sentinel at every level, so an explicit config
// value equal to the built-in default still pins it. This is what lets
// one server instance carry both the desktop window and a phone: declare
// web.bind in server.yaml and the sidecar's --bind-default no longer wins.
func resolveSetting[T comparable](flag, configVal T, configSet bool, flagDefault, unset, builtin T) T {
	switch {
	case flag != unset:
		return flag
	case configSet:
		return configVal
	case flagDefault != unset:
		return flagDefault
	default:
		return builtin
	}
}

// resolveBind applies resolveSetting to the web door ("" = unset).
func resolveBind(flagBind, configBind string, configBindSet bool, bindDefault string) string {
	return resolveSetting(flagBind, configBind, configBindSet, bindDefault, "", config.Defaults.Web.Bind)
}

// resolveFederationPort applies resolveSetting to the node door (-1 =
// unset; 0 is a real value, an ephemeral port).
func resolveFederationPort(flagPort, configPort int, configSet bool, portDefault int) int {
	return resolveSetting(flagPort, configPort, configSet, portDefault, -1, config.Defaults.Federation.Port)
}

// bindWarning is the exposure notice for a non-loopback WEB bind with no
// password: every byte of the UI and its API is open on that interface.
// With a password the web door is gated, and since 2026-08-26 the gRPC
// node export is a separate loopback-only listener — nothing else is on
// the address, so nothing else to warn about. Loopback: never a warning.
func bindWarning(addr string, hasPassword bool) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	if host == "localhost" {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return ""
	}
	if hasPassword {
		return ""
	}
	return fmt.Sprintf(`gridwell: WARNING: web UI listening on %s — this is NOT a loopback address.
gridwell: WARNING: the web UI and its API are UNAUTHENTICATED: anyone who can reach that
gridwell: WARNING: address can read and write every tile and open live shell PTYs on this machine.
gridwell: WARNING: set web.password in server.yaml, and bind a VPN-only address (e.g. your
gridwell: WARNING: Tailscale IP), never an open network.`, addr)
}

// servingBanner is the one-line boot contract with the desktop sidecar
// (apps/desktop/src/main/lines.ts parses it): the web door's ACTUAL bound
// address leads, printed only once both listeners are up; federation= is
// the node door's actual loopback address (what the shell relay dials).
// With a password configured it also carries the derived auth token
// (server.AuthToken — the cookie value), so the sidecar can authenticate
// its own window without ever prompting: local stdout is same-trust as
// server.yaml, which holds the password itself.
func servingBanner(addr, fedAddr, staticDir string, plugins int, password string) string {
	if staticDir == "" {
		staticDir = "embedded"
	}
	if password == "" {
		return fmt.Sprintf("gridwell: serving on %s (static=%s plugins=%d federation=%s)", addr, staticDir, plugins, fedAddr)
	}
	return fmt.Sprintf("gridwell: serving on %s (static=%s plugins=%d federation=%s auth=%s)",
		addr, staticDir, plugins, fedAddr, server.AuthToken(password))
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

// injectConnections carries server.yaml's connections: declarations to
// the builtin transport through the one flat config vocabulary (v2
// #269). Exactly one remote entry may exist when the key is present —
// two transports sharing one connection list would double-materialize.
func injectConnections(cfg *config.ServerConfig) error {
	if !cfg.ConnectionsSet {
		return nil
	}
	var remotes []*config.PluginConfig
	for i := range cfg.Plugins {
		if cfg.Plugins[i].Kind == "remote" {
			remotes = append(remotes, &cfg.Plugins[i])
		}
	}
	if len(remotes) == 0 {
		if len(cfg.Connections) > 0 {
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
	specs := make([]remote.ConnSpec, 0, len(cfg.Connections))
	for _, c := range cfg.Connections {
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

// resolvePluginBinaries fills in Binary for every plugin that didn't pin
// one explicitly in server.yaml, by kind — skipping kinds a bundled
// binary provides in-process (its factories win: that is the composer's
// whole choice; an explicit server.yaml binary: still beats both).
func resolvePluginBinaries(cfg *config.ServerConfig, factories map[string]plugin.ServerFactory, providers map[string]plugin.ProviderFactory) error {
	for i := range cfg.Plugins {
		pc := &cfg.Plugins[i]
		if pc.Binary != "" {
			continue
		}
		// PLUGIN factories satisfy plugin entries only: a provider entry
		// serves a different service, so a bundled binary's in-process
		// plugin factory must not suppress resolving the provider BINARY
		// (found 2026-08-23: gridwell-all + a provider home failed with
		// "no provider factory" because the fs plugin factory swallowed
		// the lookup).
		if _, ok := factories[pc.Kind]; ok && !pc.Provider {
			continue
		}
		name := "gridwell-plugin-" + pc.Kind
		if pc.Provider {
			// A bundled binary's in-process PROVIDER factory satisfies a
			// provider entry the same way a plugin factory satisfies a
			// plugin entry.
			if _, ok := providers[pc.Kind]; ok {
				continue
			}
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

// RunServeWith starts the backend HTTP server — the data plane for the
// Gridwell desktop app and any plain-browser client: Connect-RPC, the SSE
// event stream, the wasm client, and shell PTYs. Live URL tiles are hosted
// natively by the Electron shell, so there is no browser driver here. The
// listen address comes from resolveBind (loopback by default; server.yaml
// web.bind pins it, e.g. to a Tailscale IP for phone access). SIGINT/SIGTERM
// trigger graceful shutdown.
//
// factories is the BUNDLED-binary door (a leaf composer, docs/plugin.md):
// kinds present in it load in-process through the compose door; everything
// else spawns out-of-process. The stock host passes nil.
func RunServeWith(args []string, factories map[string]plugin.ServerFactory, providers map[string]plugin.ProviderFactory) int {
	// The v2 folds: the native store (local) and the builtin transport
	// (remote) are node code, always in-process. A composer's own
	// factories (mobile) still win.
	factories = node.WithNativeTransports(factories)
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
	cfg.Web.Bind = resolveBind(f.Bind, cfg.Web.Bind, cfg.Web.BindSet, f.BindDefault)
	cfg.Federation.Port = resolveFederationPort(f.FederationPort, cfg.Federation.Port, cfg.Federation.PortSet, f.FederationPortDefault)
	cfg.StaticDir = f.StaticDir
	for _, d := range cfg.Deprecations {
		fmt.Fprintf(os.Stderr, "gridwell: DEPRECATED in %s: %s\n", cfgPath, d)
	}

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
	if err := injectConnections(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		return 1
	}
	if err := resolvePluginBinaries(cfg, factories, providers); err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		return 1
	}

	// The node core (internal/node — shared with the mobile bind): plugin
	// loading, identity, the server assembly, and the two listeners (the
	// web door where config says, the federation door on loopback). The
	// CLI's own concerns wrap it: the lock above, the banner below,
	// signals.
	n, err := node.Start(node.Options{
		Home:              home,
		Cfg:               cfg,
		Factories:         factories,
		ProviderFactories: providers,
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
	if w := bindWarning(n.Ln.Addr().String(), cfg.Web.Password != ""); w != "" {
		fmt.Fprintln(os.Stderr, w)
	}
	banner := servingBanner(n.Ln.Addr().String(), n.FedLn.Addr().String(), cfg.StaticDir, len(cfg.Plugins), cfg.Web.Password)
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
