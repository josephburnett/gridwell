package tmux

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

// --- Pure-Go tests: name encoding + argv composition ---

// TestSessionNameRoundtrip locks in the canonical tile-id → tmux-
// session-name mapping. Any change to this encoding silently breaks
// orphan cleanup (which parses names from `tmux list-sessions`) and
// makes pre-existing sessions un-attachable after a server upgrade.
func TestSessionNameRoundtrip(t *testing.T) {
	// Qualified ids contain "/" and the uuid may contain "-"/"_"; the encoding
	// must round-trip all of them.
	for _, id := range []string{"localdb-uuid/1", "a1b2c3d4/7", "my_db/1000", "gridwell-root/2147483647"} {
		name := SessionName(id)
		got, ok := ParseSessionName(name)
		if !ok || got != id {
			t.Errorf("SessionName(%q) = %q; ParseSessionName roundtrip got (%q, %v)", id, name, got, ok)
		}
	}
}

// TestParseSessionNameRejectsNonGridwell guards against the orphan
// cleanup blowing away sessions in the user's own tmux that happen
// to share our socket (shouldn't happen given private socket, but
// defensive).
func TestParseSessionNameRejectsNonGridwell(t *testing.T) {
	// Non-gridwell names, the bare prefix (empty payload), and invalid base64
	// must all be rejected.
	cases := []string{"", "main", "work", "gridwell-", "gridwell-!@#$", "gridwell-not base64"}
	for _, name := range cases {
		if _, ok := ParseSessionName(name); ok {
			t.Errorf("ParseSessionName(%q) = ok; want not-ok", name)
		}
	}
}

// TestArgsCreate locks in the exact argv used for fresh-tile spawn.
// If tmux ever changes its flag semantics (e.g. -A on new-session)
// or if we drift, the wasm UX silently breaks: a "refresh fresh
// tile" might attach to stale state from a previous test run on the
// same socket, etc.
func TestArgsCreate(t *testing.T) {
	c := &Controller{binary: "/usr/bin/tmux", socketName: "test", configPath: "/tmp/cfg.conf", shell: "bash"}
	got := c.Args("t/42", ModeCreate, 80, 24, "/home/joe")
	want := []string{
		"/usr/bin/tmux", "-L", "test", "-f", "/tmp/cfg.conf",
		"new-session", "-A", "-s", SessionName("t/42"),
		"-c", "/home/joe",
		"-x", "80", "-y", "24", "bash",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ModeCreate argv mismatch\n got=%v\nwant=%v", got, want)
	}
}

// TestArgsCreateSkipsCwdWhenEmpty: when no startDir is given, bash
// inherits the spawned process's cwd. Asserting on the omission
// catches regressions where an empty -c "" leaks into argv (tmux
// would create the session in / rather than $HOME).
func TestArgsCreateSkipsCwdWhenEmpty(t *testing.T) {
	c := &Controller{binary: "tmux", socketName: "s", configPath: "/tmp/c", shell: "bash"}
	got := c.Args("t/1", ModeCreate, 80, 24, "")
	for _, a := range got {
		if a == "-c" {
			t.Errorf("argv contained -c with empty startDir: %v", got)
		}
	}
}

// TestArgsAttach locks in the attach-only form. Crucially does NOT
// include -A: that's the bug-class where a "refresh of a tile whose
// session is gone" silently spawns a fresh bash. Per the design,
// attach must fail in that case so the wasm hides the button.
func TestArgsAttach(t *testing.T) {
	c := &Controller{binary: "tmux", socketName: "s", configPath: "/tmp/c"}
	got := c.Args("t/7", ModeAttach, 80, 24, "/ignored")
	want := []string{
		"tmux", "-L", "s", "-f", "/tmp/c",
		"attach-session", "-t", SessionName("t/7"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ModeAttach argv mismatch\n got=%v\nwant=%v", got, want)
	}
}

// TestFilterEnvDropsTmuxVar guards against the recursion case: if
// gridwell ever runs inside an outer tmux, that outer's $TMUX would
// leak into our shell-out and tmux refuses commands on a different
// socket from inside an existing session.
func TestFilterEnvDropsTmuxVar(t *testing.T) {
	in := []string{"PATH=/usr/bin", "TMUX=/tmp/tmux-1000/default,1234,0", "HOME=/home/x"}
	out := filterEnv(in, "TMUX")
	for _, e := range out {
		if strings.HasPrefix(e, "TMUX=") {
			t.Errorf("TMUX leaked through filterEnv: %v", out)
		}
	}
	if len(out) != 2 {
		t.Errorf("filterEnv dropped wrong count: got %d, want 2", len(out))
	}
}

// TestIsMissingSessionErrMatchesTmuxMessages: the sentinel detection
// is what makes HasSession("missing") return (false, nil) rather
// than bubbling tmux's exit-1 as an error. Keeping it tested means
// a tmux version that rewords its diagnostic won't silently break
// the whole "is the session alive?" path.
func TestIsMissingSessionErrMatchesTmuxMessages(t *testing.T) {
	cases := []string{
		"can't find session: gridwell-5",
		"session not found: gridwell-5",
		"no such session: gridwell-5",
		"Can't find session: gridwell-5\n",
	}
	for _, msg := range cases {
		if !isMissingSessionErr([]byte(msg), errors.New("exit 1")) {
			t.Errorf("isMissingSessionErr(%q) = false; want true", msg)
		}
	}
	// Real failure must not be classified as "missing".
	if isMissingSessionErr([]byte("permission denied"), errors.New("exit 1")) {
		t.Error(`isMissingSessionErr("permission denied") = true; want false`)
	}
	// nil error never matches.
	if isMissingSessionErr([]byte("can't find session"), nil) {
		t.Error("isMissingSessionErr with nil err = true; want false")
	}
}

// TestConfigFileWrittenWithExpectedDirectives: the on-disk config
// is what enforces the no-status-bar, 50k-scrollback, no-escape-
// delay behavior. If it ever drifts (e.g. we forget to set status
// off), shell tiles would gain a chrome-y bottom bar the user
// explicitly said not to show.
func TestConfigFileWrittenWithExpectedDirectives(t *testing.T) {
	c, cleanup, err := New("gridwell-test-cfg", "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	b, err := os.ReadFile(c.configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, must := range []string{
		"status off",
		"history-limit 50000",
		`default-terminal "xterm-256color"`,
		"escape-time 0",
	} {
		if !strings.Contains(string(b), must) {
			t.Errorf("config missing directive %q. full content:\n%s", must, string(b))
		}
	}
}

// TestCleanupRemovesConfigFile catches the leak where a long-lived
// server cycles many controllers (tests, hot reload) and forgets to
// rm its temp configs.
func TestCleanupRemovesConfigFile(t *testing.T) {
	c, cleanup, err := New("gridwell-test-cleanup", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.configPath); err != nil {
		t.Fatalf("config not written: %v", err)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.configPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("config still exists after cleanup: %v", err)
	}
}

// --- Integration tests: require a real tmux binary on PATH ---

// requireTmux skips the test if tmux isn't installed. The package-
// level tests (above) cover all the pure logic; these check that
// real tmux honors our flags as documented.
func requireTmux(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH; skipping integration test")
	}
}

// newTestController gives each integration test its own private
// socket so concurrent runs don't see each other's sessions. The
// returned cleanup kills the tmux server (taking all its sessions
// with it) and removes the config file.
func newTestController(t *testing.T) *Controller {
	t.Helper()
	socket := fmt.Sprintf("gridwell-test-%d-%s", os.Getpid(), strings.ReplaceAll(t.Name(), "/", "-"))
	c, cleanup, err := New(socket, "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Kill the server (and all its sessions) before removing the
		// config so we don't leave a server hanging.
		_, _ = c.run("kill-server")
		_ = cleanup()
	})
	return c
}

// TestHasSessionMissingReturnsFalseNoErr: the empty-socket case
// (tmux server not running, no sessions) is the steady-state at
// program startup, not an error.
func TestHasSessionMissingReturnsFalseNoErr(t *testing.T) {
	requireTmux(t)
	c := newTestController(t)
	alive, err := c.HasSession("t/42")
	if err != nil {
		t.Fatalf("HasSession with no server: err=%v; want nil", err)
	}
	if alive {
		t.Errorf("HasSession with no session = true; want false")
	}
}

// TestCreateThenHasSessionThenKill is the full lifecycle: spawn a
// session via Args (the same path the WS handler uses), confirm
// HasSession sees it, kill it, confirm gone. Establishes that our
// argv vector actually does what we documented.
func TestCreateThenHasSessionThenKill(t *testing.T) {
	requireTmux(t)
	c := newTestController(t)

	// Spawn a session by exec'ing the create argv detached. We use
	// `-d` to skip the attach (we just want to provoke session
	// creation; no client needed).
	createArgs := c.Args("t/101", ModeCreate, 80, 24, "")
	// Insert -d to keep the session detached for the test.
	createArgs = injectDetach(createArgs)
	if out, err := exec.Command(createArgs[0], createArgs[1:]...).CombinedOutput(); err != nil {
		t.Fatalf("create session: %v: %s", err, out)
	}

	alive, err := c.HasSession("t/101")
	if err != nil || !alive {
		t.Fatalf("HasSession(101) after create = (%v, %v); want (true, nil)", alive, err)
	}

	if err := c.KillSession("t/101"); err != nil {
		t.Fatalf("KillSession: %v", err)
	}

	alive, err = c.HasSession("t/101")
	if err != nil || alive {
		t.Errorf("HasSession after kill = (%v, %v); want (false, nil)", alive, err)
	}
}

// TestKillSessionMissingIsNoOp: the orphan cleanup path calls Kill
// on session ids that may or may not exist; "already gone" must not
// be an error.
func TestKillSessionMissingIsNoOp(t *testing.T) {
	requireTmux(t)
	c := newTestController(t)
	if err := c.KillSession("t/99999"); err != nil {
		t.Errorf("KillSession on missing = %v; want nil", err)
	}
}

// TestListSessionsEmptyNoServer: at server first-launch the tmux
// server isn't running; this must return an empty list (not error).
// Without this, the orphan cleanup pass would fail noisily on
// brand-new installs.
func TestListSessionsEmptyNoServer(t *testing.T) {
	requireTmux(t)
	c := newTestController(t)
	ids, err := c.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions with no server: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("ListSessions with no server = %v; want empty", ids)
	}
}

// TestListSessionsEnumeratesGridwellOnly: ListSessions must skip
// any session not matching the gridwell-N pattern. Without this,
// orphan cleanup would mass-kill the user's own sessions if they
// somehow share our socket.
func TestListSessionsEnumeratesGridwellOnly(t *testing.T) {
	requireTmux(t)
	c := newTestController(t)

	mustExec(t, c.Args("t/7", ModeCreate, 80, 24, ""))
	mustExec(t, c.Args("t/11", ModeCreate, 80, 24, ""))
	mustExec(t, c.run, "new-session", "-d", "-s", "user-session")

	ids, err := c.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, id := range ids {
		got[id] = true
	}
	if !got["t/7"] || !got["t/11"] {
		t.Errorf("ListSessions = %v; missing gridwell tiles 7, 11", ids)
	}
	if len(got) != 2 {
		t.Errorf("ListSessions included non-gridwell entries: %v", ids)
	}
}

// TestAttachModeFailsWhenSessionMissing is the headline behavioral
// guarantee for the "shell session is gone" path: an attach attempt
// against a missing session must exit non-zero so the WS handler's
// shelldriver.Start returns an error, so the WS handler tells the
// client "not alive", so the refresh button hides. tmux's actual
// diagnostic in this case is "no sessions" (when the server has no
// sessions at all) — covered by isMissingSessionErr's matcher.
func TestAttachModeFailsWhenSessionMissing(t *testing.T) {
	requireTmux(t)
	c := newTestController(t)
	args := c.Args("t/404", ModeAttach, 80, 24, "")
	cmd := exec.Command(args[0], args[1:]...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("attach to missing session succeeded; want failure. out=%s", out)
	}
	if !isMissingSessionErr(out, err) && !isNoServerErr(out, err) {
		t.Errorf("attach to missing session: want missing/no-server diagnostic, got: %s (err=%v)", out, err)
	}
}

// --- test helpers ---

// injectDetach inserts the `-d` flag right after the `new-session`
// verb so the create runs as a detached server side-effect (no
// client attach). Used to provoke session creation from tests
// without needing a PTY.
func injectDetach(args []string) []string {
	for i, a := range args {
		if a == "new-session" {
			out := make([]string, 0, len(args)+1)
			out = append(out, args[:i+1]...)
			out = append(out, "-d")
			out = append(out, args[i+1:]...)
			return out
		}
	}
	return args
}

// mustExec runs the given argv (or a single function call form) and
// fatals on error. Two-arg form fits c.Args output; three-arg form
// fits direct c.run invocations from tests.
func mustExec(t *testing.T, run any, extra ...string) {
	t.Helper()
	switch v := run.(type) {
	case []string:
		// Argv vector. If it's a new-session, run detached.
		args := injectDetach(v)
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Env = os.Environ() // explicit; predictable env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("exec %v: %v: %s", args, err, out)
		}
	case func(...string) ([]byte, error):
		out, err := v(extra...)
		if err != nil {
			t.Fatalf("run %v: %v: %s", extra, err, out)
		}
	default:
		t.Fatalf("mustExec: unsupported run type %T", run)
	}
	// Brief settle for tmux's IPC to record state across the
	// has-session round-trip. tmux is usually instant; this guards
	// against the occasional CI flake.
	time.Sleep(20 * time.Millisecond)
}

// TestArgsCreateUsesConfiguredShell: the `shell:` plugin config key picks the
// login shell for newly-created sessions (issue #86) — the argv's final
// element, where the hardcoded "bash" used to live.
func TestArgsCreateUsesConfiguredShell(t *testing.T) {
	c := &Controller{binary: "tmux", socketName: "s", configPath: "/tmp/c", shell: "/bin/zsh"}
	got := c.Args("t/9", ModeCreate, 80, 24, "")
	if got[len(got)-1] != "/bin/zsh" {
		t.Errorf("argv shell = %q, want /bin/zsh (argv %v)", got[len(got)-1], got)
	}
}

// TestNewResolvesShell locks the one resolution point: explicit config wins,
// then $SHELL, then the bash fallback.
func TestNewResolvesShell(t *testing.T) {
	newShell := func(t *testing.T, cfg string) string {
		t.Helper()
		c, cleanup, err := New("gridwell-test-shell", "", cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cleanup() })
		return c.shell
	}
	t.Run("config wins over env", func(t *testing.T) {
		t.Setenv("SHELL", "/bin/zsh")
		if got := newShell(t, "fish"); got != "fish" {
			t.Errorf("shell = %q, want fish", got)
		}
	})
	t.Run("falls back to $SHELL", func(t *testing.T) {
		t.Setenv("SHELL", "/bin/zsh")
		if got := newShell(t, ""); got != "/bin/zsh" {
			t.Errorf("shell = %q, want /bin/zsh", got)
		}
	})
	t.Run("falls back to bash", func(t *testing.T) {
		t.Setenv("SHELL", "")
		if got := newShell(t, ""); got != "bash" {
			t.Errorf("shell = %q, want bash", got)
		}
	})
}

// TestBrowserShimScript (issue #90): the gridwell-open shim New() writes must
// emit the url as an OSC 5522 sequence on stdout — tmux-passthrough-wrapped
// (ESCs doubled, DCS tmux; wrapper) when $TMUX is set, bare otherwise. Run
// the real script through sh: this is the seam the terminal parses.
func TestBrowserShimScript(t *testing.T) {
	c, cleanup, err := New("gridwell-test-shim", "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cleanup() })
	if c.browserShim == "" {
		t.Fatal("no browser shim path")
	}

	run := func(tmuxEnv string) string {
		cmd := exec.Command("sh", c.browserShim, "https://example.com/x")
		cmd.Env = append(os.Environ(), "TMUX="+tmuxEnv)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("run shim (TMUX=%q): %v", tmuxEnv, err)
		}
		return string(out)
	}

	bare := run("")
	if want := "\x1b]5522;https://example.com/x\x1b\\"; bare != want {
		t.Errorf("bare = %q, want %q", bare, want)
	}
	wrapped := run("/tmp/tmux-sock,123,0")
	if want := "\x1bPtmux;\x1b\x1b]5522;https://example.com/x\x1b\x1b\\\x1b\\"; wrapped != want {
		t.Errorf("wrapped = %q, want %q", wrapped, want)
	}
}

// TestShadowLaunchers (issue #166): $BROWSER alone is not enough — emacs
// browse-url execs xdg-open directly, and a desktop-backed xdg-open resolves
// via the DE handler without ever reading $BROWSER. New() must write a shadow
// bin dir of launcher scripts that (a) hand web urls to the gridwell-open
// shim and (b) fall through to the REAL command of the same name for
// everything else (xdg-open opens files too). Run the real scripts through
// sh: this is the seam emacs crosses.
func TestShadowLaunchers(t *testing.T) {
	c, cleanup, err := New("gridwell-test-shadow", "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cleanup() })
	if c.shadowDir == "" {
		t.Fatal("no shadow launcher dir")
	}

	wantNames := []string{"xdg-open", "gnome-open", "kde-open",
		"x-www-browser", "www-browser", "sensible-browser"}
	for _, name := range wantNames {
		p := c.shadowDir + "/" + name
		st, err := os.Stat(p)
		if err != nil {
			t.Fatalf("shadow launcher %s: %v", name, err)
		}
		if st.Mode().Perm()&0o111 == 0 {
			t.Errorf("shadow launcher %s not executable: %v", name, st.Mode())
		}
	}

	// (a) a web url is handed to the shim, which emits the OSC 5522 sequence
	// (bare here — TMUX unset). DE mode must not matter: the whole point is
	// intercepting BEFORE xdg-open's desktop dispatch.
	cmd := exec.Command(c.shadowDir+"/xdg-open", "https://example.com/x")
	cmd.Env = append(filterEnv(os.Environ(), "TMUX"), "XDG_CURRENT_DESKTOP=GNOME")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("shadow xdg-open url: %v", err)
	}
	if want := "\x1b]5522;https://example.com/x\x1b\\"; string(out) != want {
		t.Errorf("shadow xdg-open url = %q, want %q", out, want)
	}

	// (b) a non-url argument falls through to the real command found on PATH
	// with the shadow dir stripped — no self-exec loop, host workflows keep
	// working. The "real" xdg-open here records its argv to a marker file.
	realDir := t.TempDir()
	marker := realDir + "/marker"
	real := "#!/bin/sh\nprintf '%s' \"$*\" > " + marker + "\n"
	if err := os.WriteFile(realDir+"/xdg-open", []byte(real), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command(c.shadowDir+"/xdg-open", "/tmp/some-file.pdf")
	cmd.Env = append(filterEnv(os.Environ(), "TMUX", "PATH"),
		"PATH="+c.shadowDir+":"+realDir+":/usr/bin:/bin")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("shadow xdg-open file fallthrough: %v (%s)", err, out)
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("real xdg-open never ran: %v", err)
	}
	if string(got) != "/tmp/some-file.pdf" {
		t.Errorf("real xdg-open argv = %q, want %q", got, "/tmp/some-file.pdf")
	}

	// cleanup removes the whole shadow dir.
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := os.Stat(c.shadowDir); !os.IsNotExist(err) {
		t.Errorf("shadow dir survives cleanup: %v", err)
	}
}

// TestEnvPrependsShadowPath (issue #166): the tmux CLIENT env must carry the
// shadow bin dir at the front of PATH — panes inherit PATH from the tmux
// SERVER process (tmux 3.5a ignores `-e PATH=`/set-environment for panes),
// and the client Env() feeds is what lazy-starts that server.
func TestEnvPrependsShadowPath(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("TERM", "")
	c := &Controller{binary: "tmux", socketName: "s", configPath: "/tmp/c",
		shell: "bash", browserShim: "/tmp/gridwell-open", shadowDir: "/tmp/gw-shadow"}
	var gotPath, gotTerm string
	for _, e := range c.Env() {
		if v, ok := strings.CutPrefix(e, "PATH="); ok {
			gotPath = v
		}
		if v, ok := strings.CutPrefix(e, "TERM="); ok {
			gotTerm = v
		}
	}
	if gotPath != "/tmp/gw-shadow:/usr/bin:/bin" {
		t.Errorf("Env PATH = %q, want shadow-prefixed", gotPath)
	}
	// Passing an explicit env bypasses shelldriver's TERM fallback; Env must
	// preserve that behavior itself.
	if gotTerm != "xterm-256color" {
		t.Errorf("Env TERM = %q, want xterm-256color default", gotTerm)
	}
}

// TestArgsCreateInjectsBrowserEnv: new sessions carry BROWSER pointing at the
// shim (tmux new-session -e), so terminal apps that launch a browser hand the
// url back to gridwell instead of spawning one on the host.
func TestArgsCreateInjectsBrowserEnv(t *testing.T) {
	c := &Controller{binary: "tmux", socketName: "s", configPath: "/tmp/c",
		shell: "bash", browserShim: "/tmp/gridwell-open"}
	got := c.Args("t/9", ModeCreate, 80, 24, "")
	found := false
	for i, a := range got {
		if a == "-e" && i+1 < len(got) && got[i+1] == "BROWSER=/tmp/gridwell-open" {
			found = true
		}
	}
	if !found {
		t.Errorf("argv missing -e BROWSER=<shim>: %v", got)
	}
}

// TestConfigAllowsPassthrough: the shim's OSC rides tmux's DCS passthrough,
// which is off by default — the gridwell config must enable it.
func TestConfigAllowsPassthrough(t *testing.T) {
	if !strings.Contains(gridwellConfig, "allow-passthrough on") {
		t.Error("gridwell tmux config must set allow-passthrough on")
	}
}
