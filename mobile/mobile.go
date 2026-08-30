// Package mobile is the gomobile-bindable Gridwell node: every device is a
// node. The app calls Start with its app-private directory; a full Gridwell
// node — plugins, node identity, the embedded web client, the source cache —
// comes up on a loopback port and the host webview loads the returned origin.
// The phone's own tiles live in its own store, durable with no network
// anywhere, and other machines are what they are everywhere else: mounts,
// stale-readable through the source cache when the network is down.
//
// Three things are shaped by the platform:
//   - Plugins load in-process, through node.Options.Factories, because iOS
//     forbids fork and exec, so the go-plugin subprocess model cannot exist
//     there. It is the same wire contract and the same id discipline, with no
//     process boundary. Desktop keeps subprocesses.
//   - Shells are off, because iOS has neither a PTY nor tmux: home runs with
//     no shell manager and the server refuses shell tiles node-wide. That is
//     about what the phone can host, not what its client can reach — the web
//     client attaches a PTY over the web door like any other host, so a phone
//     pointed at a desktop node has real shells.
//   - There is no serve lock, since the OS gives each app one process.
//
// First run mints the node's id through the same door serve uses,
// node.BuildConfig, so a phone home is byte-compatible with every other home.
//
// The exported surface is deliberately gomobile-shaped: strings and error, no
// structs. Start is idempotent while running and returns the same origin; Stop
// is idempotent always.
package mobile

import (
	"errors"
	"os"
	"path/filepath"
	"sync"

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

// Start brings the node up for the given home directory, the app's private
// storage, created if absent, and returns the URL the webview should load: the
// token login on the loopback origin, such as
// "http://127.0.0.1:53712/login?token=<hex>". The web door is always
// password-gated and the first serve mints the password; the webview owns its
// own cookie jar, so the first load authenticates it and redirects home.
// Idempotent while running.
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
	// First run has no server.yaml: BuildConfig mints the node's id and writes
	// the file, exactly as `gridwell serve` does on a fresh home.
	cfgPath := filepath.Join(home, "server.yaml")
	cfg, err := node.BuildConfig(home, cfgPath)
	if err != nil {
		return "", err
	}
	cfg.Web.Bind = "127.0.0.1:0"
	// The federation door stays closed on the phone: nobody mounts it, and
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
	// A serve error after a successful listen surfaces on the next Start,
	// with the node torn down. The webview's failed loads are the user-facing
	// signal, exactly as for a dead remote server.
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

// Stop shuts the node down: a bounded drain, then plugin close. Idempotent.
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

// inProcessFactories is the mobile plugin registry: fs, proc, and gitlab by
// their FromConfig, the same function each subprocess main hands guest.Main,
// so the two doors cannot derive a plugin differently.
func inProcessFactories() map[string]plugin.Factory {
	return map[string]plugin.Factory{
		"fs":     fsplugin.FromConfig,
		"proc":   procplugin.FromConfig,
		"gitlab": gitlabplugin.FromConfig,
	}
}
