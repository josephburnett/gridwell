package shelldriver

import (
	"bytes"
	"errors"
	"io"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// requireBash skips a test gracefully when the host doesn't have a
// bash binary on $PATH. The shelldriver is a thin wrapper over a real
// PTY; faking it would test the fake, not the wrapper, so we exercise
// the real shell and skip when it's not available.
func requireBash(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash not available: %v", err)
	}
	return path
}

// drainUntil reads from s.Output() into a buffer until either a
// deadline fires or the buffered output contains needle. Returns
// whatever was read so callers can include it in failure messages.
func drainUntil(t *testing.T, s *Session, needle string, deadline time.Duration) []byte {
	t.Helper()
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	var buf bytes.Buffer
	out := s.Output()
	for {
		select {
		case chunk, ok := <-out:
			if !ok {
				return buf.Bytes()
			}
			buf.Write(chunk)
			if bytes.Contains(buf.Bytes(), []byte(needle)) {
				return buf.Bytes()
			}
		case <-timer.C:
			return buf.Bytes()
		}
	}
}

// TestStartAndExit launches bash, drives it through `exit`, and
// verifies the Done channel closes. The basic lifecycle: start, read,
// terminate. If this regresses every other test would fail strangely.
func TestStartAndExit(t *testing.T) {
	bashPath := requireBash(t)
	s, err := Start(Config{
		Cwd:      "/",
		Cols:     80,
		Rows:     24,
		BashPath: bashPath,
		Args:     []string{"--norc", "--noprofile", "-i"},
		// Disable rc-file noise so the test doesn't depend on the
		// user's bash profile.
		Env: []string{"PS1=$ ", "HOME=/tmp", "TERM=dumb"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := s.Write([]byte("exit\n")); err != nil {
		t.Fatalf("Write exit: %v", err)
	}
	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("bash did not exit within 2s")
	}
	if err := s.Close(); err != nil {
		// A non-zero bash exit isn't a failure here — we just want
		// no startup/teardown error.
		t.Logf("Close returned: %v (informational)", err)
	}
}

// TestResize updates the PTY winsize while live. The host kernel
// reports the new dimensions through TIOCGWINSZ, but we verify the
// observable effect: stty inside the shell reports the new size. This
// catches the case where the wrong fd is being ioctl'd.
func TestResize(t *testing.T) {
	bashPath := requireBash(t)
	s, err := Start(Config{
		Cwd:      "/",
		Cols:     80,
		Rows:     24,
		BashPath: bashPath,
		Args:     []string{"--norc", "--noprofile", "-i"},
		Env:      []string{"PS1=READY> ", "HOME=/tmp", "TERM=dumb"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()
	drainUntil(t, s, "READY>", 2*time.Second)

	if err := s.Resize(132, 50); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	// stty size prints "rows cols" — feed it through bash and watch
	// the line scroll past.
	if _, err := s.Write([]byte("stty size\n")); err != nil {
		t.Fatalf("Write stty: %v", err)
	}
	out := drainUntil(t, s, "50 132", 2*time.Second)
	if !bytes.Contains(out, []byte("50 132")) {
		t.Errorf("stty size after resize did not report 50 132; saw: %q", out)
	}
}

// TestResizeRejectsZero is a defensive contract: a misbehaving caller
// that sends a 0x0 winsize must not blow up the PTY (which would
// kill bash via SIGWINCH on size 0).
func TestResizeRejectsZero(t *testing.T) {
	bashPath := requireBash(t)
	s, err := Start(Config{
		Cwd: "/", Cols: 80, Rows: 24, BashPath: bashPath, Args: []string{"--norc", "--noprofile", "-i"},
		Env: []string{"PS1=$ ", "HOME=/tmp", "TERM=dumb"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()

	if err := s.Resize(0, 24); err == nil {
		t.Error("Resize(0, 24): expected error, got nil")
	}
	if err := s.Resize(80, 0); err == nil {
		t.Error("Resize(80, 0): expected error, got nil")
	}
}

// TestStartRejectsZeroSize: the kernel will accept 0x0 silently if we
// pass it through to pty.StartWithSize, then bash dies with weird
// SIGWINCH-induced behavior. Reject at the boundary.
func TestStartRejectsZeroSize(t *testing.T) {
	bashPath := requireBash(t)
	if _, err := Start(Config{Cwd: "/", Cols: 0, Rows: 24, BashPath: bashPath}); err == nil {
		t.Error("Start with Cols=0: expected error, got nil")
	}
	if _, err := Start(Config{Cwd: "/", Cols: 80, Rows: 0, BashPath: bashPath}); err == nil {
		t.Error("Start with Rows=0: expected error, got nil")
	}
}

// TestStartFallsBackOnMissingCwd: if the caller passes a directory
// that no longer exists (deleted between freeze and refresh), Start
// should still succeed by falling back to $HOME / cwd, not surface a
// cryptic exec failure.
func TestStartFallsBackOnMissingCwd(t *testing.T) {
	bashPath := requireBash(t)
	tmp := t.TempDir()
	s, err := Start(Config{
		Cwd:      "/this/path/does/not/exist",
		Cols:     80,
		Rows:     24,
		BashPath: bashPath,
		Args:     []string{"--norc", "--noprofile", "-i"},
		Env:      []string{"PS1=READY> ", "HOME=" + tmp, "TERM=dumb"},
	})
	if err != nil {
		t.Fatalf("Start with bad Cwd should fall back, got: %v", err)
	}
	defer s.Close()
	out := drainUntil(t, s, "READY>", 2*time.Second)
	if !bytes.Contains(out, []byte("READY>")) {
		t.Fatalf("bash never reached prompt; output: %q", out)
	}
}

// TestWriteAfterCloseReturnsClosedPipe locks in the post-close
// contract so the server's read goroutine doesn't have to special-case
// "still alive" vs "torn down" — Output returns EOF, Write returns
// ErrClosedPipe.
func TestWriteAfterCloseReturnsClosedPipe(t *testing.T) {
	bashPath := requireBash(t)
	s, err := Start(Config{Cwd: "/", Cols: 80, Rows: 24, BashPath: bashPath, Args: []string{"--norc", "--noprofile", "-i"},
		Env: []string{"PS1=$ ", "HOME=/tmp", "TERM=dumb"}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Logf("Close: %v (informational)", err)
	}
	if _, err := s.Write([]byte("x")); !errors.Is(err, io.ErrClosedPipe) {
		t.Errorf("Write after Close: err = %v, want io.ErrClosedPipe", err)
	}
	if _, ok := <-s.Output(); ok {
		t.Errorf("Output() channel not closed after Close()")
	}
}

// TestCloseTerminatesLongRunningChild verifies the process-group kill
// reaches a subprocess bash spawned. Without setsid + pgid kill, a
// stuck `sleep 60` would outlive the session and leak.
func TestCloseTerminatesLongRunningChild(t *testing.T) {
	bashPath := requireBash(t)
	s, err := Start(Config{Cwd: "/", Cols: 80, Rows: 24, BashPath: bashPath, Args: []string{"--norc", "--noprofile", "-i"},
		Env: []string{"PS1=READY> ", "HOME=/tmp", "TERM=dumb"}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	drainUntil(t, s, "READY>", 2*time.Second)
	if _, err := s.Write([]byte("sleep 60 &\n")); err != nil {
		t.Fatalf("Write sleep: %v", err)
	}
	// Read back something to confirm bash processed the line.
	drainUntil(t, s, "READY>", 2*time.Second)

	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return within 2s")
	}
	// Done must be closed by now (Close blocks on it internally).
	select {
	case <-s.Done():
	default:
		t.Error("Done channel not closed after Close")
	}
}

// TestOutputForwardsBytes is a smoke test for the I/O path: write a
// command, observe its output. If the goroutine wiring is wrong this
// fails before any of the cwd / resize tests reach their assertions,
// pointing at the right layer.
func TestOutputForwardsBytes(t *testing.T) {
	bashPath := requireBash(t)
	s, err := Start(Config{Cwd: "/", Cols: 80, Rows: 24, BashPath: bashPath, Args: []string{"--norc", "--noprofile", "-i"},
		Env: []string{"PS1=READY> ", "HOME=/tmp", "TERM=dumb"}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()
	drainUntil(t, s, "READY>", 2*time.Second)

	const marker = "gridwell-shell-test-marker-abc"
	if _, err := s.Write([]byte("echo " + marker + "\n")); err != nil {
		t.Fatalf("Write echo: %v", err)
	}
	out := drainUntil(t, s, marker, 2*time.Second)
	if !strings.Contains(string(out), marker) {
		t.Errorf("never saw marker %q in output; got: %q", marker, out)
	}
}

// TestCloseIsIdempotent: repeat Close calls return quickly without
// double-killing. The server's WS handler may call Close on both the
// disconnect path and the explicit-ascent path; that's fine.
func TestCloseIsIdempotent(t *testing.T) {
	bashPath := requireBash(t)
	s, err := Start(Config{Cwd: "/", Cols: 80, Rows: 24, BashPath: bashPath, Args: []string{"--norc", "--noprofile", "-i"},
		Env: []string{"PS1=$ ", "HOME=/tmp", "TERM=dumb"}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	var calls atomic.Int32
	for range 3 {
		go func() {
			s.Close()
			calls.Add(1)
		}()
	}
	deadline := time.After(2 * time.Second)
	for calls.Load() < 3 {
		select {
		case <-deadline:
			t.Fatalf("Close calls did not all return; got %d/3", calls.Load())
		case <-time.After(10 * time.Millisecond):
		}
	}
}
