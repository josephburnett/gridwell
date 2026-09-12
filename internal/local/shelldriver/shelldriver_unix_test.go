//go:build unix

package shelldriver

import (
	"bytes"
	"errors"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// requireBash skips a test when the host has no bash on $PATH. The driver is
// a thin wrapper over a real PTY, so a fake would only test the fake.
func requireBash(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash not available: %v", err)
	}
	return path
}

// drainUntil reads s.Output() into a buffer until the deadline fires or the
// buffer contains needle. It returns what it read, for failure messages.
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

// Pins the lifecycle: bash starts, takes a written `exit`, and closes Done.
func TestStartAndExit(t *testing.T) {
	bashPath := requireBash(t)
	s, err := Start(Config{
		Cwd:      "/",
		Cols:     80,
		Rows:     24,
		BashPath: bashPath,
		Args:     []string{"--norc", "--noprofile", "-i"},
		// A fixed prompt, HOME, and TERM, so the test does not depend on
		// the user's bash profile or environment.
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
		// A non-zero bash exit is not a failure here. Only a startup or
		// teardown error would be.
		t.Logf("Close returned: %v (informational)", err)
	}
}

// Pins the observable effect of a live resize: stty inside the shell reports
// the new size, which catches an ioctl on the wrong fd.
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
	// stty size prints "rows cols".
	if _, err := s.Write([]byte("stty size\n")); err != nil {
		t.Fatalf("Write stty: %v", err)
	}
	out := drainUntil(t, s, "50 132", 2*time.Second)
	if !bytes.Contains(out, []byte("50 132")) {
		t.Errorf("stty size after resize did not report 50 132; saw: %q", out)
	}
}

// A 0x0 winsize reaches the PTY as a SIGWINCH that kills bash, so a
// misbehaving caller's zero is refused instead.
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

// pty.StartWithSize accepts 0x0 silently, so Start rejects it at the
// boundary; see TestResizeRejectsZero for what a zero does to bash.
func TestStartRejectsZeroSize(t *testing.T) {
	bashPath := requireBash(t)
	if _, err := Start(Config{Cwd: "/", Cols: 0, Rows: 24, BashPath: bashPath}); err == nil {
		t.Error("Start with Cols=0: expected error, got nil")
	}
	if _, err := Start(Config{Cwd: "/", Cols: 80, Rows: 0, BashPath: bashPath}); err == nil {
		t.Error("Start with Rows=0: expected error, got nil")
	}
}

// A Cwd deleted between freeze and refresh must still start, through
// resolveCwd's fallback, rather than surface a cryptic exec failure.
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

// Pins the post-close contract, Output at EOF and Write refusing with
// ErrClosedPipe, so a reader goroutine needs no live-or-torn-down case split.
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
	// Drain to close. Chunks produced before the fd closed may still arrive
	// after Close; see Session.Output.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-s.Output():
			if !ok {
				return // closed, so the contract holds
			}
		case <-deadline:
			t.Fatal("Output() not closed within 5s of Close()")
		}
	}
}

// Without setsid and the process-group kill, a `sleep 60` bash spawned would
// outlive the session and leak.
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
	// Read back a prompt to confirm bash processed the line.
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

// Close is reached from more than one teardown path, the disconnect and the
// explicit ascent, so repeat calls must return without double-killing.
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

// Pins the wedged-pump goroutine leak. With outCh full and the subscriber
// gone, a blocking `outCh <- chunk` strands the pump when Close runs, because
// closing the PTY unblocks a blocked Read and leaves a blocked send alone.
// Each cycle spews output nobody drains and then Closes, which is the shape of
// a tile deleted while undrained; many cycles so one stranded goroutine per
// cycle stands out above runtime noise.
func TestPumpDoesNotLeakWhenOutputUndrained(t *testing.T) {
	bashPath := requireBash(t)
	const cycles = 8

	// Settle and capture a baseline goroutine count.
	time.Sleep(100 * time.Millisecond)
	runtime.GC()
	base := runtime.NumGoroutine()

	for range cycles {
		s, err := Start(Config{
			Cwd: "/", Cols: 80, Rows: 24, BashPath: bashPath,
			Args: []string{"--norc", "--noprofile", "-i"},
		})
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		// Spew unbounded output and never read s.Output(), so the pump fills
		// outCh and blocks on the next send.
		if _, err := s.Write([]byte("yes\n")); err != nil {
			t.Fatalf("Write: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
		// Close returns bash's exit status, "signal: killed" here, which
		// this test does not care about.
		_ = s.Close()
	}

	// A wedged pump would leave one extra goroutine per cycle, so wait for
	// exiting goroutines to wind down before comparing against the baseline.
	deadline := time.Now().Add(5 * time.Second)
	for {
		runtime.GC()
		n := runtime.NumGoroutine()
		if n <= base+2 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pump goroutines leaked: baseline %d, now %d after %d undrained Close cycles", base, n, cycles)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// setSigtermGrace rewrites the SIGTERM grace for one test and restores it.
func setSigtermGrace(t *testing.T, d time.Duration) time.Duration {
	t.Helper()
	was := sigtermGrace
	t.Cleanup(func() { sigtermGrace = was })
	sigtermGrace = d
	return sigtermGrace
}

// startScript runs one bash script on a real PTY.
func startScript(t *testing.T, script string) *Session {
	t.Helper()
	s, err := Start(Config{
		Cwd:      "/",
		Cols:     80,
		Rows:     24,
		BashPath: requireBash(t),
		Args:     []string{"--norc", "--noprofile", "-c", script},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return s
}

// A shell that traps SIGTERM and keeps running is killed, and the grace is
// what it costs: Close waits sigtermGrace before SIGKILL and no longer, so a
// closing pane never hangs on a process that will not leave.
func TestCloseKillsAProcessThatTrapsSIGTERMAtTheGrace(t *testing.T) {
	grace := setSigtermGrace(t, 200*time.Millisecond)
	s := startScript(t, `trap "" TERM; echo trapped; while :; do sleep 0.05; done`)
	// The marker is the trap being installed: a SIGTERM sent before bash runs
	// the trap line kills it outright and times nothing.
	if out := drainUntil(t, s, "trapped", 10*time.Second); !bytes.Contains(out, []byte("trapped")) {
		t.Fatalf("the shell never installed the trap: %q", out)
	}
	// Drained, because Close's kill path waits on the process, not on a
	// reader: a full output channel must not be what the timing measures.
	go func() {
		for range s.Output() {
		}
	}()

	done := make(chan time.Duration, 1)
	start := time.Now()
	go func() {
		_ = s.Close()
		done <- time.Since(start)
	}()
	var took time.Duration
	select {
	case took = <-done:
	case <-time.After(3 * grace / 2):
		t.Fatalf("Close was still waiting after %v — SIGKILL must follow sigtermGrace (%v)", 3*grace/2, grace)
	}
	if took < grace {
		t.Fatalf("Close returned after %v, before sigtermGrace (%v) — a shell gets the whole grace to exit on SIGTERM", took, grace)
	}
	select {
	case <-s.Done():
	default:
		t.Fatal("Close returned with the process still running")
	}
}

// The other side of the same grace: a shell that honors SIGTERM is gone inside
// it, so the wait is a ceiling on one that hangs and never a delay on a pane
// closing normally.
func TestCloseReturnsWhenAProcessHonorsSIGTERM(t *testing.T) {
	// Generous, because what is asserted is that none of it is spent.
	grace := setSigtermGrace(t, 2*time.Second)
	s := startScript(t, `while :; do sleep 0.05; done`)
	go func() {
		for range s.Output() {
		}
	}()

	start := time.Now()
	_ = s.Close()
	if took := time.Since(start); took >= grace {
		t.Fatalf("Close took %v; a shell that honors SIGTERM must not wait out sigtermGrace (%v)", took, grace)
	}
}
