// Package mobile is the gomobile-bindable Gridwell node (offline-plan
// phase 2: EVERY DEVICE IS A NODE). The Flutter app calls Start with its
// app-private directory; a full Gridwell node — plugins, node identity,
// the embedded web client, the mount cache — comes up on a loopback port
// and the host webview loads the returned origin. The phone's own tiles
// live in its own localdb, durable with no network anywhere; other
// machines are what they are everywhere else — mounts, stale-readable
// through the phase-1 cache when the tailnet is down.
//
// Mobile-shaped by decision (docs/offline-plan.md):
//   - Plugins load IN-PROCESS (node.Options.Factories): iOS forbids
//     fork/exec, so the go-plugin subprocess model cannot exist there.
//     Same wire contract, same id discipline, no process boundary —
//     promoted from the loader's test-only path to the supported mobile
//     mode. Desktop keeps subprocesses.
//   - Shells are OFF (no PTY, no tmux); the localdb plugin runs with no
//     shell manager and the server refuses shell tiles node-wide.
//   - No password (loopback inside an app sandbox; the webview is the
//     only client) and no serve lock (the OS gives each app one process).
//   - First run auto-inits a localdb named "home" through the SAME init
//     door the CLI uses (node.InitPlugin), so a phone home is
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
	"sync"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/node"
	"github.com/josephburnett/gridwell/internal/plugin"
	fsplugin "github.com/josephburnett/gridwell/internal/plugin/fs"
	"github.com/josephburnett/gridwell/internal/plugin/localdb"
	"github.com/josephburnett/gridwell/internal/plugin/proc"
	"github.com/josephburnett/gridwell/internal/plugin/sshdial"
	"github.com/josephburnett/gridwell/internal/plugin/sshhost"
	"github.com/josephburnett/gridwell/web"
)

var (
	mu      sync.Mutex
	running *node.Node
	origin  string
)

// Start brings the node up for the given home directory (the app's
// private storage; created if absent) and returns the loopback origin the
// webview should load, e.g. "http://127.0.0.1:53712". Idempotent while
// running.
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
		if _, err := node.InitPlugin(home, "localdb", "home", nil); err != nil {
			return "", fmt.Errorf("mobile: first-run init: %w", err)
		}
	}
	cfg, err := node.BuildConfig(home, cfgPath)
	if err != nil {
		return "", err
	}
	cfg.Bind = "127.0.0.1:0"
	cfg.DisableShells = true
	n, err := node.Start(node.Options{
		Home:      home,
		Cfg:       cfg,
		Factories: inProcessFactories(home),
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
	origin = "http://" + n.Ln.Addr().String()
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

// inProcessFactories is the mobile plugin registry: every kind the
// platform supports, constructed exactly as its subprocess main would
// (cmd/plugin/*), minus the process boundary — and minus shells (no
// tmux manager; the server refuses shell tiles anyway).
func inProcessFactories(home string) map[string]plugin.ServerFactory {
	return map[string]plugin.ServerFactory{
		"localdb": func(pc *config.PluginConfig) (gridwellv1.GridwellServer, error) {
			st, err := localdb.OpenVerified(pc.Config["db_file"], pc.ID, pc.Kind)
			if err != nil {
				return nil, err
			}
			return localdb.New(st, nil), nil
		},
		"fs":   fsplugin.NewFactory,
		"proc": proc.NewFactory,
		"ssh": func(pc *config.PluginConfig) (gridwellv1.GridwellServer, error) {
			db, err := sshhost.OpenDB(pc.Config["db_file"])
			if err != nil {
				return nil, err
			}
			return sshhost.New(db, sshdial.Dial, home), nil
		},
	}
}
