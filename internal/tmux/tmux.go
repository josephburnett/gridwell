// Package tmux owns the gridwell-private tmux server: its socket
// location, its config file, and the commands needed to create,
// attach, kill, and enumerate sessions. The package does NOT spawn
// PTYs — it composes the argv vectors for `tmux ...` invocations and
// shells out for the metadata operations (has-session, list-sessions,
// kill-session). The PTY-attaching exec is driven by shelldriver.
//
// One Controller corresponds to one tmux server. By using `-L
// <socket>` we get a server isolated from the user's daily tmux,
// which means: arbitrary user .tmux.conf doesn't leak into shell
// tiles, gridwell can kill ALL its sessions with one `kill-server`,
// and the namespace can't collide with anything the user runs by
// hand. By using `-f <our-config>` we lock the prefix, scrollback,
// status-bar, and TERM choices to what gridwell expects rather than
// inheriting whatever the user's home config says.
package tmux

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// gridwellConfig is the tmux server config gridwell writes once at
// New(). It deliberately avoids any user-facing chrome:
//
//   - status off: the bottom status bar would duplicate Gridwell's
//     own pane chrome.
//   - history-limit 50000: scrollback per pane (the standard default
//     is 2000, far too small for a workspace shell).
//   - default-terminal: xterm.js claims xterm-256color upstream;
//     match it here so $TERM is sane inside bash.
//   - escape-time 0: kill the meta-key delay (tmux's default 500ms
//     interferes with terminal apps reading raw escape sequences).
//   - allow-passthrough on: the gridwell-open browser shim (issue #90)
//     emits its OSC 5522 url sequence through tmux's DCS passthrough,
//     which is off by default.
const gridwellConfig = `set-option -g status off
set-option -g history-limit 50000
set-option -g default-terminal "xterm-256color"
set-option -g escape-time 0
set-option -g allow-passthrough on
`

// browserShimScript is the $BROWSER target injected into every new shell
// session (issue #90): instead of launching a browser on the host, it hands
// the url back to the gridwell terminal as an OSC 5522 sequence, which the
// client turns into an ephemeral url descent. Inside tmux the sequence rides
// the DCS passthrough wrapper (inner ESCs doubled), matching what
// allow-passthrough unwraps for the outer terminal.
const browserShimScript = `#!/bin/sh
# gridwell-open: hand a url back to the gridwell terminal (issue #90).
url="$1"
if [ -n "$TMUX" ]; then
	printf '\033Ptmux;\033\033]5522;%s\033\033\\\033\\' "$url"
else
	printf '\033]5522;%s\033\\' "$url"
fi
`

// Controller is one gridwell-owned tmux server. Construct with New;
// the returned cleanup removes the on-disk config file.
type Controller struct {
	// binary is the path to the tmux executable. Allows tests to
	// substitute a stub.
	binary string
	// socketName is passed to `tmux -L <name>`. Combined with the
	// runtime socket dir (default /tmp/tmux-<uid>), this gives the
	// server a deterministic, gridwell-only address.
	socketName string
	// configPath is the file passed to `tmux -f <path>`. Lives under
	// os.TempDir for the lifetime of the controller.
	configPath string
	// shell is the login shell spawned inside a newly-created session.
	// Resolved once in New (plugin config -> $SHELL -> "bash"); Args uses it
	// verbatim. Only ModeCreate consults it — existing tmux sessions keep
	// whatever shell they were created with.
	shell string
	// browserShim is the path of the gridwell-open script (written by New
	// alongside the config); ModeCreate injects it as $BROWSER so terminal
	// apps hand urls back to gridwell instead of spawning a host browser.
	browserShim string
}

// New initializes a Controller on the given socket name. The config
// file is written to a fresh path under os.TempDir; the returned
// cleanup func removes it. Returns an error only on filesystem
// failures — tmux itself is not invoked here (the server is lazy-
// started by the first command).
//
// binary may be "" to default to "tmux" looked up on $PATH. Tests
// pass a fully-qualified path or a stub.
//
// shell is the login shell for newly-created sessions (the plugin's `shell:`
// config key). "" falls back to $SHELL, then "bash" — so a Mac whose login
// shell is zsh gets zsh with zero config. This is the one resolution point;
// the choice only applies to sessions created AFTER it takes effect (existing
// tmux sessions persist with their original shell).
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
	f, err := os.CreateTemp("", "gridwell-tmux-*.conf")
	if err != nil {
		return nil, nil, fmt.Errorf("tmux: write config: %w", err)
	}
	if _, err := f.WriteString(gridwellConfig); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, nil, fmt.Errorf("tmux: write config: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return nil, nil, fmt.Errorf("tmux: write config: %w", err)
	}
	shim, err := os.CreateTemp("", "gridwell-open-*.sh")
	if err != nil {
		_ = os.Remove(f.Name())
		return nil, nil, fmt.Errorf("tmux: write browser shim: %w", err)
	}
	if _, err := shim.WriteString(browserShimScript); err != nil {
		_ = shim.Close()
		_ = os.Remove(shim.Name())
		_ = os.Remove(f.Name())
		return nil, nil, fmt.Errorf("tmux: write browser shim: %w", err)
	}
	if err := shim.Close(); err != nil {
		_ = os.Remove(shim.Name())
		_ = os.Remove(f.Name())
		return nil, nil, fmt.Errorf("tmux: write browser shim: %w", err)
	}
	if err := os.Chmod(shim.Name(), 0o755); err != nil {
		_ = os.Remove(shim.Name())
		_ = os.Remove(f.Name())
		return nil, nil, fmt.Errorf("tmux: chmod browser shim: %w", err)
	}
	c := &Controller{
		binary:      binary,
		socketName:  socketName,
		configPath:  f.Name(),
		shell:       shell,
		browserShim: shim.Name(),
	}
	cleanup := func() error {
		err1 := os.Remove(c.configPath)
		if err2 := os.Remove(c.browserShim); err1 == nil {
			err1 = err2
		}
		return err1
	}
	return c, cleanup, nil
}

// Args returns the argv for spawning a tmux client connected to the
// session named by tileID. Suitable for handing to shelldriver.Start
// (the first element is the executable; the rest are its arguments).
//
// When mode is ModeCreate the session is created if missing (`-A`
// flag on new-session): used for fresh tiles where no snapshot yet
// exists. When mode is ModeAttach the client attaches to an EXISTING
// session and tmux exits non-zero if the session is gone: used for
// snapshotted tiles where "session gone" must surface to the wasm so
// the refresh button can hide.
//
// startDir is the cwd for the shell process inside a newly-created
// session; ignored on attach (tmux already remembers where the shell
// is). Empty defaults to $HOME inside the spawned process.
//
// cols and rows seed the tmux window size; tmux propagates SIGWINCH
// from later client resizes through the existing PTY interface.
func (c *Controller) Args(tileID string, mode Mode, cols, rows uint16, startDir string) []string {
	name := SessionName(tileID)
	args := []string{c.binary, "-L", c.socketName, "-f", c.configPath}
	switch mode {
	case ModeCreate:
		args = append(args, "new-session", "-A", "-s", name)
		if startDir != "" {
			args = append(args, "-c", startDir)
		}
		if c.browserShim != "" {
			// Terminal apps that launch a browser (emacs browse-url,
			// xdg-open) consult $BROWSER: point it at the gridwell-open
			// shim so the url comes back as an OSC and descends into an
			// ephemeral url tile instead of opening Chrome (issue #90).
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

// Mode is the create-or-attach choice the WS handler makes per
// refresh. The wasm side decides which by combining ShellSessionAlive
// with the tile's PreviewBlobID; see internal/server/shell_stream.go.
type Mode int

const (
	// ModeCreate creates the session if missing; attaches otherwise.
	// Used for fresh tiles (no snapshot yet) — the only case where
	// silently spawning a new bash is the right behavior.
	ModeCreate Mode = iota
	// ModeAttach attaches to an existing session and fails if gone.
	// Used when the tile has been snapshotted: silently spawning
	// fresh state would discard whatever the user thought was still
	// running.
	ModeAttach
)

// SessionName is the canonical mapping from a (qualified) tile id to a tmux
// session name. The tile id is the globally-qualified "<plugin-uuid>/<id>" so
// shells in different localdb plugins never collide. It is base64url-encoded
// because tmux session names can't contain "/" / "." / ":"; the encoding is
// reversible (ParseSessionName) so the orphan sweep can map a session back to
// its tile id. Stable across restarts so the same tile reattaches.
func SessionName(tileID string) string {
	return "gridwell-" + base64.RawURLEncoding.EncodeToString([]byte(tileID))
}

// ParseSessionName is the inverse of SessionName. Returns the qualified tile id
// and ok=true when name matches the gridwell prefix and decodes cleanly. Used
// by the startup orphan cleanup to map listed sessions back to tile ids.
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

// HasSession reports whether the gridwell session for tileID exists
// on this controller's socket. Implemented via `tmux has-session`
// which exits 0 if present, non-zero otherwise. The error return is
// non-nil only on infrastructure failures (binary missing,
// unwritable config) — a normal "session does not exist", AND the
// "no server running yet" first-launch state, both yield
// (false, nil). They're semantically equivalent for the caller.
//
// Concurrency: safe to call from multiple goroutines; tmux's IPC
// serializes commands on the socket.
func (c *Controller) HasSession(tileID string) (bool, error) {
	name := SessionName(tileID)
	out, err := c.run("has-session", "-t", name)
	if err == nil {
		return true, nil
	}
	if isMissingSessionErr(out, err) || isNoServerErr(out, err) {
		return false, nil
	}
	return false, fmt.Errorf("tmux has-session %s: %w", name, err)
}

// KillSession terminates the session for tileID. No-op if the
// session is already gone OR the tmux server isn't running.
// Returns an error only on infrastructure failures.
func (c *Controller) KillSession(tileID string) error {
	name := SessionName(tileID)
	out, err := c.run("kill-session", "-t", name)
	if err == nil {
		return nil
	}
	if isMissingSessionErr(out, err) || isNoServerErr(out, err) {
		return nil
	}
	return fmt.Errorf("tmux kill-session %s: %w", name, err)
}

// ListSessions returns the tile ids of all gridwell-prefixed
// sessions on this controller's socket. Sessions whose names don't
// match the gridwell pattern are silently ignored. An empty result
// is returned with no error when the tmux server isn't running yet
// (first-launch state).
func (c *Controller) ListSessions() ([]string, error) {
	out, err := c.run("list-sessions", "-F", "#{session_name}")
	if err != nil {
		if isNoServerErr(out, err) {
			return nil, nil
		}
		return nil, fmt.Errorf("tmux list-sessions: %w", err)
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

// PaneCommand returns the command running in the foreground of the tile's
// tmux session — what tmux shows as the window's automatic name (e.g.
// "claude", "vim", "bash"). Returns "" with no error when the session is
// gone or the server isn't running, so callers can simply skip relabeling.
func (c *Controller) PaneCommand(tileID string) (string, error) {
	name := SessionName(tileID)
	out, err := c.run("display-message", "-t", name, "-p", "#{pane_current_command}")
	if err != nil {
		if isMissingSessionErr(out, err) || isNoServerErr(out, err) {
			return "", nil
		}
		return "", fmt.Errorf("tmux display-message %s: %w", name, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// run executes `tmux -L <socket> -f <config> <args...>`. Returns
// combined stdout+stderr so callers can sniff for "no session" /
// "no server" sentinels. Inherits the parent process's environment
// minus TMUX so the gridwell server can run inside the user's own
// tmux without recursing.
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

// isMissingSessionErr distinguishes "tmux says no such session"
// from any other failure. tmux's exact wording varies by command:
//
//   - has-session / kill-session for a missing session by name:
//     "can't find session: NAME"
//   - attach-session when there are no sessions at all:
//     "no sessions"
//   - older / non-OpenBSD ports occasionally use "session not found"
//     or "no such session"
//
// Match all of them so a renamed diagnostic in a future tmux release
// doesn't silently break the "is the session alive?" path.
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

// isNoServerErr distinguishes "tmux server isn't running yet" — a
// totally expected first-launch state — from a real failure. The
// observed messages on tmux 3.x are:
//
//   - "no server running on /tmp/tmux-NNN/socketname" (some commands)
//   - "error connecting to /tmp/tmux-NNN/socketname (No such file or
//     directory)" (others, when the socket file is missing)
//
// Both mean the same thing: nothing exists for us to talk to.
func isNoServerErr(out []byte, err error) bool {
	if err == nil {
		return false
	}
	low := strings.ToLower(string(out))
	return strings.Contains(low, "no server running") ||
		strings.Contains(low, "error connecting to ") ||
		strings.Contains(low, "no such file or directory")
}

// filterEnv returns env minus any entry whose key matches one of
// drop. Used to strip TMUX from the spawned process so a tmux server
// running our process never causes recursion when we shell out for
// metadata commands.
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
