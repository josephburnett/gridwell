// Package tmux owns the gridwell-private tmux server. It spawns no PTYs;
// shelldriver drives the PTY-attaching exec. One Controller is one tmux
// server, isolated by `-L <socket>` so a user .tmux.conf never leaks into a
// shell tile and session names collide with nothing run by hand.
package tmux

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// gridwellConfig is written once at New:
//
//   - status off: a status bar would duplicate Gridwell's pane chrome.
//   - history-limit: the default 2000 is too small for a working shell.
//   - default-terminal: what xterm.js claims upstream.
//   - escape-time 0: the 500ms meta delay interferes with apps reading raw
//     escape sequences.
//   - allow-passthrough on: the shim's OSC 5522 rides tmux's DCS passthrough.
//   - mouse on: without it xterm.js keeps the client in the alternate buffer,
//     where a wheel is arrow keys. Apps requesting mouse reporting still
//     receive it through passthrough.
const gridwellConfig = `set-option -g status off
set-option -g history-limit 50000
set-option -g default-terminal "xterm-256color"
set-option -g escape-time 0
set-option -g allow-passthrough on
set-option -g mouse on
`

// browserShimScript is the $BROWSER target injected into every new session. It
// hands the url back as an OSC 5522 sequence, which the client turns into an
// ephemeral url descent. Inside tmux that rides the DCS passthrough wrapper
// with inner ESCs doubled.
const browserShimScript = `#!/bin/sh
# gridwell-open: hand a url back to the gridwell terminal (issue #90).
url="$1"
if [ -n "$TMUX" ]; then
	printf '\033Ptmux;\033\033]5522;%s\033\033\\\033\\' "$url"
else
	printf '\033]5522;%s\033\\' "$url"
fi
`

// shadowLauncherNames go in front of PATH because $BROWSER alone is not
// enough: emacs browse-url execs xdg-open directly, and a desktop-backed
// xdg-open resolves the handler without reading $BROWSER. gio stays unshadowed,
// every flow reaching `gio open` passing the shadowed xdg-open first.
var shadowLauncherNames = []string{
	"xdg-open", "gnome-open", "kde-open",
	"x-www-browser", "www-browser", "sensible-browser",
}

// shadowLauncherScript sends web urls to the shim (the first %q) and falls
// through to the real command of the same name by stripping the shadow dir
// (the second %q) from PATH, so it can never exec itself.
const shadowLauncherScript = `#!/bin/sh
# gridwell shadow launcher (issue #166): web urls come back to gridwell.
case "$1" in
http://*|https://*)
	exec %q "$1"
	;;
esac
newpath=
IFS=:
for d in $PATH; do
	[ "$d" = %q ] && continue
	newpath="${newpath:+$newpath:}$d"
done
unset IFS
export PATH="$newpath"
exec "$(basename "$0")" "$@"
`

// Controller is one gridwell-owned tmux server. Construct with New.
type Controller struct {
	// binary: tests point it at a stub.
	binary     string
	socketName string
	configPath string
	// shell: only ModeCreate consults it, an existing session keeping the
	// shell it was created with.
	shell string
	// browserShim is injected as $BROWSER by ModeCreate.
	browserShim string
	// shadowDir catches programs that exec a system opener instead of
	// reading $BROWSER.
	shadowDir string
}

// New errors only on filesystem failures, tmux not being invoked here: the
// server is lazy-started by the first command. It is the one place shell is
// resolved: "" falls back to $SHELL, then "bash".
func New(socketName, binary, shell string) (*Controller, func() error, error) {
	if socketName == "" {
		return nil, nil, errors.New("tmux: socketName must be non-empty")
	}
	if binary == "" {
		binary = "tmux"
	}
	if shell == "" {
		shell = os.Getenv("SHELL")
	}
	if shell == "" {
		shell = "bash"
	}
	// Stable per-socket paths. A per-boot temp path would leak an artifact
	// per start and let a /tmp cleaner delete a running session's shim.
	dir := filepath.Join(os.TempDir(), "gridwell-tmux-"+socketName)
	shadowDir := filepath.Join(dir, "shadow-bin")
	if err := os.MkdirAll(shadowDir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("tmux: config dir: %w", err)
	}
	confPath := filepath.Join(dir, "tmux.conf")
	if err := os.WriteFile(confPath, []byte(gridwellConfig), 0o600); err != nil {
		return nil, nil, fmt.Errorf("tmux: write config: %w", err)
	}
	shimPath := filepath.Join(dir, "gridwell-open.sh")
	if err := os.WriteFile(shimPath, []byte(browserShimScript), 0o755); err != nil {
		return nil, nil, fmt.Errorf("tmux: write browser shim: %w", err)
	}
	if err := os.Chmod(shimPath, 0o755); err != nil {
		return nil, nil, fmt.Errorf("tmux: chmod browser shim: %w", err)
	}
	if err := writeShadowLaunchers(shadowDir, shimPath); err != nil {
		return nil, nil, err
	}
	c := &Controller{
		binary:      binary,
		socketName:  socketName,
		configPath:  confPath,
		shell:       shell,
		browserShim: shimPath,
		shadowDir:   shadowDir,
	}
	cleanup := func() error {
		return os.RemoveAll(dir)
	}
	return c, cleanup, nil
}

// writeShadowLaunchers overwrites idempotently.
func writeShadowLaunchers(dir, shimPath string) error {
	body := fmt.Sprintf(shadowLauncherScript, shimPath, dir)
	for _, name := range shadowLauncherNames {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			return fmt.Errorf("tmux: write shadow launcher %s: %w", name, err)
		}
		if err := os.Chmod(p, 0o755); err != nil {
			return fmt.Errorf("tmux: chmod shadow launcher %s: %w", name, err)
		}
	}
	return nil
}

// Args is the argv for shelldriver.Start. ModeAttach exits non-zero if the
// session is gone, so that surfaces to the wasm and the refresh button can
// hide. startDir is ignored on attach and "" defaults to $HOME; later resizes
// ride SIGWINCH through the PTY.
func (c *Controller) Args(key string, mode Mode, cols, rows uint16, startDir string) []string {
	name := SessionName(key)
	args := []string{c.binary, "-L", c.socketName, "-f", c.configPath}
	switch mode {
	case ModeCreate:
		args = append(args, "new-session", "-A", "-s", name)
		if startDir != "" {
			args = append(args, "-c", startDir)
		}
		if c.browserShim != "" {
			// See browserShimScript.
			args = append(args, "-e", "BROWSER="+c.browserShim)
		}
		args = append(args,
			"-x", strconv.Itoa(int(cols)),
			"-y", strconv.Itoa(int(rows)),
			c.shell)
	case ModeAttach:
		args = append(args, "attach-session", "-t", name)
	}
	return args
}

// Env carries the shadow dir on the client that may lazy-start the server,
// because tmux refuses to apply a PATH from -e or set-environment to panes; a
// server already running keeps its old PATH until it exits, and a login script
// that hard-resets PATH drops the shadow. TERM is defaulted here because an
// explicit env bypasses shelldriver's own fallback.
func (c *Controller) Env() []string {
	env := filterEnv(os.Environ(), "PATH", "TERM")
	path := os.Getenv("PATH")
	if c.shadowDir != "" {
		if path == "" {
			path = c.shadowDir
		} else {
			path = c.shadowDir + ":" + path
		}
	}
	env = append(env, "PATH="+path)
	term := os.Getenv("TERM")
	if term == "" {
		term = "xterm-256color"
	}
	env = append(env, "TERM="+term)
	return env
}

// Mode is owned by internal/local's shell stream, which derives allowCreate
// from whether any tile naming the session has a face.
type Mode int

const (
	// ModeCreate is for a session never started, the only case where
	// silently spawning a new shell is right.
	ModeCreate Mode = iota
	// ModeAttach fails if the session is gone. A started session silently
	// spawning fresh state would discard what the user thought was running.
	ModeAttach
)

// SessionName takes a session key, the namespace-local id of the tile that
// started the session, and base64url-encodes it because a tmux session name
// cannot contain "/", "." or ":". It is stable across restarts.
func SessionName(key string) string {
	return "gridwell-" + base64.RawURLEncoding.EncodeToString([]byte(key))
}

// ParseSessionName is the inverse of SessionName, for the orphan cleanup.
func ParseSessionName(name string) (string, bool) {
	const prefix = "gridwell-"
	if !strings.HasPrefix(name, prefix) {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(name[len(prefix):])
	if err != nil || len(raw) == 0 {
		return "", false
	}
	return string(raw), true
}

// HasSession errors only on infrastructure failures. Both "session does not
// exist" and the first-launch "no server running yet" yield (false, nil). It
// is safe to call concurrently; tmux's IPC serializes on the socket.
func (c *Controller) HasSession(key string) (bool, error) {
	name := SessionName(key)
	out, err := c.run("has-session", "-t", name)
	if err == nil {
		return true, nil
	}
	if isMissingSessionErr(out, err) || isNoServerErr(out, err) {
		return false, nil
	}
	return false, fmt.Errorf("tmux has-session %s: %w (output: %q)", name, err, strings.TrimSpace(string(out)))
}

// KillSession is a no-op if the session or the server is already gone.
func (c *Controller) KillSession(key string) error {
	name := SessionName(key)
	out, err := c.run("kill-session", "-t", name)
	if err == nil {
		return nil
	}
	if isMissingSessionErr(out, err) || isNoServerErr(out, err) {
		return nil
	}
	return fmt.Errorf("tmux kill-session %s: %w (output: %q)", name, err, strings.TrimSpace(string(out)))
}

// ListSessions ignores names that do not match the gridwell pattern; an empty
// result with no error means the server is not running yet.
func (c *Controller) ListSessions() ([]string, error) {
	out, err := c.run("list-sessions", "-F", "#{session_name}")
	if err != nil {
		if isNoServerErr(out, err) {
			return nil, nil
		}
		return nil, fmt.Errorf("tmux list-sessions: %w (output: %q)", err, strings.TrimSpace(string(out)))
	}
	var ids []string
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if id, ok := ParseSessionName(line); ok {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// PaneCommand is tmux's automatic window name, "" with no error when the
// session is gone so callers skip relabeling.
func (c *Controller) PaneCommand(key string) (string, error) {
	name := SessionName(key)
	out, err := c.run("display-message", "-t", name, "-p", "#{pane_current_command}")
	if err != nil {
		if isMissingSessionErr(out, err) || isNoServerErr(out, err) {
			return "", nil
		}
		return "", fmt.Errorf("tmux display-message %s: %w (output: %q)", name, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// run returns combined output so callers can sniff for the "no session" and
// "no server" sentinels, and drops TMUX so the gridwell server can run inside
// the user's own tmux without recursing.
func (c *Controller) run(args ...string) ([]byte, error) {
	full := append([]string{"-L", c.socketName, "-f", c.configPath}, args...)
	cmd := exec.Command(c.binary, full...)
	cmd.Env = filterEnv(os.Environ(), "TMUX")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.Bytes(), err
}

// isMissingSessionErr matches every wording tmux uses, so a renamed diagnostic
// in a future release does not silently break the liveness path.
func isMissingSessionErr(out []byte, err error) bool {
	if err == nil {
		return false
	}
	low := strings.ToLower(string(out))
	return strings.Contains(low, "can't find session") ||
		strings.Contains(low, "session not found") ||
		strings.Contains(low, "no such session") ||
		strings.Contains(low, "no sessions")
}

// isNoServerErr matches both tmux 3.x wordings for a server not running yet.
func isNoServerErr(out []byte, err error) bool {
	if err == nil {
		return false
	}
	low := strings.ToLower(string(out))
	return strings.Contains(low, "no server running") ||
		strings.Contains(low, "error connecting to ") ||
		strings.Contains(low, "no such file or directory")
}

// filterEnv drops TMUX; see run.
func filterEnv(env []string, drop ...string) []string {
	out := make([]string, 0, len(env))
	dropSet := map[string]bool{}
	for _, d := range drop {
		dropSet[d] = true
	}
	for _, e := range env {
		key := e
		if i := strings.IndexByte(e, '='); i > 0 {
			key = e[:i]
		}
		if dropSet[key] {
			continue
		}
		out = append(out, e)
	}
	return out
}
