// Package mobile is the gomobile-bindable Gridwell node (offline-plan
// phase 2: EVERY DEVICE IS A NODE). The Flutter app calls Start with its
// app-private directory; a full Gridwell node — plugins, node identity,
// the embedded web client, the mount cache — comes up on a loopback port
// and the host webview loads the returned origin. The phone's own tiles
// live in its own local plugin DB, durable with no network anywhere; other
// machines are what they are everywhere else — mounts, stale-readable
// through the phase-1 cache when the tailnet is down.
//
// Mobile-shaped by decision (docs/offline-plan.md):
//   - Providers load IN-PROCESS (node.Options.Factories): iOS forbids
//     fork/exec, so the go-plugin subprocess model cannot exist there.
//     Same wire contract, same id discipline, no process boundary —
//     promoted from the loader's test-only path to the supported mobile
//     mode. Desktop keeps subprocesses.
//   - Shells are OFF (no PTY, no tmux); the local plugin runs with no
//     shell manager and the server refuses shell tiles node-wide.
//   - No password (loopback inside an app sandbox; the webview is the
//     only client) and no serve lock (the OS gives each app one process).
//   - First run auto-inits a "local" plugin named "home" through the SAME init
//     door the CLI uses (node.Init), so a phone home is
//     byte-compatible with every other home.
//
// The exported surface is deliberately gomobile-shaped: strings, error,
// no structs. Start is idempotent while running (returns the same
// origin); Stop is idempotent always.
package mobile

import (
	"errors"
	"fmt"
	gofs "io/fs"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/internal/node"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/server"
	fsplugin "github.com/josephburnett/gridwell/plugins/fs/plugin"
	gitlabplugin "github.com/josephburnett/gridwell/plugins/gitlab/plugin"
	procplugin "github.com/josephburnett/gridwell/plugins/proc/plugin"
	"github.com/josephburnett/gridwell/web"
)

var (
	mu      sync.Mutex
	running *node.Node
	origin  string
)

// Start brings the node up for the given home directory (the app's
// private storage; created if absent) and returns the URL the webview
// should load: the TOKEN LOGIN on the loopback origin, e.g.
// "http://127.0.0.1:53712/login?token=<hex>" — the web door is always
// password-gated (2026-08-26; first-run init mints the password), and the
// webview owns its own cookie jar, so the first load authenticates it and
// redirects home. Idempotent while running.
func Start(home string) (string, error) {
	mu.Lock()
	defer mu.Unlock()
	if running != nil {
		return origin, nil
	}
	if home == "" {
		return "", errors.New("mobile: home directory required")
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", err
	}
	cfgPath := filepath.Join(home, "server.yaml")
	if _, err := os.Stat(cfgPath); errors.Is(err, gofs.ErrNotExist) {
		// First run: one localdb named "home", exactly the heal the
		// desktop sidecar performs — same door, same resulting bytes.
		if _, err := node.Init(home, "local", "home", nil); err != nil {
			return "", fmt.Errorf("mobile: first-run init: %w", err)
		}
	}
	cfg, err := node.BuildConfig(home, cfgPath)
	if err != nil {
		return "", err
	}
	cfg.Web.Bind = "127.0.0.1:0"
	// The federation door stays CLOSED on the phone: nobody mounts it, and
	// iOS container paths overrun the unix-socket path limit anyway.
	cfg.Federation.Socket = ""
	cfg.DisableShells = true
	n, err := node.Start(node.Options{
		Factories: inProcessFactories(),
		Home:      home,
		Cfg:       cfg,
		StaticFS:  web.FS,
	})
	if err != nil {
		return "", err
	}
	// Serve errors after a successful listen are surfaced on the next
	// Start (the node is torn down); the webview's failed loads are the
	// user-facing signal, exactly like a dead remote server today.
	errCh := n.ServeBackground()
	go func() {
		if serveErr := <-errCh; serveErr != nil {
			mu.Lock()
			if running == n {
				running = nil
				origin = ""
			}
			mu.Unlock()
		}
	}()
	running = n
	origin = server.TokenLoginURL("http://"+n.Ln.Addr().String(), cfg.WebPassword)
	return origin, nil
}

// Stop shuts the node down (bounded drain, then plugin close). Idempotent.
func Stop() {
	mu.Lock()
	defer mu.Unlock()
	if running == nil {
		return
	}
	_ = running.Close()
	running = nil
	origin = ""
}

// inProcessFactories is the mobile provider registry: fs, proc
// and gitlab as v2 content providers, constructed exactly as their subprocess
// mains (cmd/gridwell-plugin-*) would, minus the process boundary.
func inProcessFactories() map[string]plugin.Factory {
	return map[string]plugin.Factory{
		"fs": func(cfg map[string]string) (pluginv1.PluginServer, error) {
			return fsplugin.New(cfg["root"], nil), nil
		},
		"proc": func(cfg map[string]string) (pluginv1.PluginServer, error) {
			pid, _ := strconv.ParseInt(cfg["pid"], 10, 64)
			return procplugin.New("", pid, nil), nil
		},
		"gitlab": func(cfg map[string]string) (pluginv1.PluginServer, error) {
			return gitlabplugin.FromConfig(cfg), nil
		},
	}
}
