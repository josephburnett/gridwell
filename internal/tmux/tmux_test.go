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
	for _, id := range []int64{1, 7, 1000, 1<<31 - 1} {
		name := SessionName(id)
		got, ok := ParseSessionName(name)
		if !ok || got != id {
			t.Errorf("SessionName(%d) = %q; ParseSessionName roundtrip got (%d, %v)", id, name, got, ok)
		}
	}
}

// TestParseSessionNameRejectsNonGridwell guards against the orphan
// cleanup blowing away sessions in the user's own tmux that happen
// to share our socket (shouldn't happen given private socket, but
// defensive).
func TestParseSessionNameRejectsNonGridwell(t *testing.T) {
	cases := []string{"", "main", "work", "gridwell-", "gridwell-abc", "gridwell-0", "gridwell--5"}
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
	c := &Controller{binary: "/usr/bin/tmux", socketName: "test", configPath: "/tmp/cfg.conf"}
	got := c.Args(42, ModeCreate, 80, 24, "/home/joe")
	want := []string{
		"/usr/bin/tmux", "-L", "test", "-f", "/tmp/cfg.conf",
		"new-session", "-A", "-s", "gridwell-42",
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
	c := &Controller{binary: "tmux", socketName: "s", configPath: "/tmp/c"}
	got := c.Args(1, ModeCreate, 80, 24, "")
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
	got := c.Args(7, ModeAttach, 80, 24, "/ignored")
	want := []string{
		"tmux", "-L", "s", "-f", "/tmp/c",
		"attach-session", "-t", "gridwell-7",
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
	c, cleanup, err := New("gridwell-test-cfg", "")
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
	c, cleanup, err := New("gridwell-test-cleanup", "")
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
	c, cleanup, err := New(socket, "")
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
	alive, err := c.HasSession(42)
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
	createArgs := c.Args(101, ModeCreate, 80, 24, "")
	// Insert -d to keep the session detached for the test.
	createArgs = injectDetach(createArgs)
	if out, err := exec.Command(createArgs[0], createArgs[1:]...).CombinedOutput(); err != nil {
		t.Fatalf("create session: %v: %s", err, out)
	}

	alive, err := c.HasSession(101)
	if err != nil || !alive {
		t.Fatalf("HasSession(101) after create = (%v, %v); want (true, nil)", alive, err)
	}

	if err := c.KillSession(101); err != nil {
		t.Fatalf("KillSession: %v", err)
	}

	alive, err = c.HasSession(101)
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
	if err := c.KillSession(99999); err != nil {
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

	mustExec(t, c.Args(7, ModeCreate, 80, 24, ""))
	mustExec(t, c.Args(11, ModeCreate, 80, 24, ""))
	mustExec(t, c.run, "new-session", "-d", "-s", "user-session")

	ids, err := c.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]bool{}
	for _, id := range ids {
		got[id] = true
	}
	if !got[7] || !got[11] {
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
	args := c.Args(404, ModeAttach, 80, 24, "")
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
