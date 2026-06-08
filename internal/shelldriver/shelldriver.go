// Package shelldriver spawns a process attached to a PTY and bridges
// its stdin/stdout to a caller-supplied I/O surface. The server's
// WebSocket shell-stream handler wraps a Session in a duplex
// transport; tests substitute in-memory transports to exercise the
// driver without needing a real WebSocket.
//
// Scope: one Session = one PTY = one spawned process. Historically
// the spawned process was bash directly; with the tmux backing it is
// `tmux new-session` / `tmux attach-session`. The driver doesn't
// know which: it just execs the configured binary with the
// configured args. That mapping lives in the server layer.
package shelldriver

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// Config describes how a Session should be started.
type Config struct {
	// Cwd is the directory bash should start in. If empty, the driver
	// falls back to $HOME, then to the parent process's working dir.
	Cwd string
	// Cols / Rows is the initial PTY window size in character cells.
	// Both must be > 0; the caller is expected to pass real terminal
	// dimensions (the server reads them off the client's pane size).
	Cols, Rows uint16
	// BashPath is the bash binary to exec. Empty means "bash" looked up
	// on $PATH. Tests substitute a fake shell.
	BashPath string
	// Args overrides the bash command-line arguments. Empty defaults to
	// {"-i"} (interactive shell that sources the user's rc files). Tests
	// pass {"--norc", "--noprofile", "-i"} so the prompt and environment
	// are deterministic.
	Args []string
	// Env, if non-nil, overrides the spawned process's environment.
	// Empty leaves it as os.Environ().
	Env []string
}

// Session is one live bash PTY. Output reads bytes from the PTY (what
// the user sees on screen); Write sends keystrokes into bash's stdin.
// Resize updates the PTY window size when the pane resizes. Cwd reads
// /proc/<pid>/cwd live so the freeze path can persist where bash is
// before SIGTERM. Close terminates the process group cleanly.
//
// All methods are safe to call concurrently. Methods that need the PTY
// after Close has run return an error rather than panicking on a torn-
// down file descriptor.
// outputBufferFrames is the depth of the internal PTY-output channel.
// Set high enough that a short gap between WS detach and re-attach
// during a takeover doesn't drop bash output. When full, the pump
// goroutine blocks on the PTY read, which back-pressures bash — that
// is the correct behavior over silently dropping bytes that may form
// part of an ANSI escape sequence.
const outputBufferFrames = 64

type Session struct {
	cmd  *exec.Cmd
	ptmx *os.File
	pid  int

	// outCh is the single drain point for PTY bytes. Exactly one
	// internal pump goroutine writes to it; subscribers (one WS
	// handler at a time) read from it. The takeover protocol relies on
	// being able to cancel-safe select on this channel, which a
	// blocking PTY Read could not satisfy.
	outCh chan []byte

	closeOnce sync.Once
	closed    atomic.Bool
	doneCh    chan struct{}
	exitErr   error
}

// Start launches a bash session under the given Config. Returns
// immediately once exec succeeds and the PTY is ready; the caller then
// reads/writes against the returned Session.
func Start(cfg Config) (*Session, error) {
	if cfg.Cols == 0 || cfg.Rows == 0 {
		return nil, fmt.Errorf("shelldriver: cols and rows must be > 0 (got %dx%d)", cfg.Cols, cfg.Rows)
	}
	cwd := resolveCwd(cfg.Cwd)
	bashPath := cfg.BashPath
	if bashPath == "" {
		bashPath = "bash"
	}
	args := cfg.Args
	if len(args) == 0 {
		args = []string{"-i"}
	}
	cmd := exec.Command(bashPath, args...)
	cmd.Dir = cwd
	if cfg.Env != nil {
		cmd.Env = cfg.Env
	} else {
		// Make sure TERM is something sensible so bash's prompt
		// rendering isn't completely flat. xterm-256color is what
		// xterm.js claims on the client.
		env := append([]string{}, os.Environ()...)
		if os.Getenv("TERM") == "" {
			env = append(env, "TERM=xterm-256color")
		}
		cmd.Env = env
	}
	// Detach into a fresh process group so SIGINT/SIGTSTP from the
	// parent terminal aren't forwarded; we manage signals ourselves.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{
		Cols: cfg.Cols,
		Rows: cfg.Rows,
	})
	if err != nil {
		return nil, fmt.Errorf("shelldriver: start bash: %w", err)
	}
	s := &Session{
		cmd:    cmd,
		ptmx:   ptmx,
		pid:    cmd.Process.Pid,
		outCh:  make(chan []byte, outputBufferFrames),
		doneCh: make(chan struct{}),
	}
	go s.reap()
	go s.pump()
	return s, nil
}

// Output returns the channel of PTY-output byte chunks. Each chunk is
// a fresh slice owned by the receiver — no aliasing of an internal
// buffer. The channel is closed when the bash process has exited or
// Close has run, so a `for chunk := range s.Output() {}` loop
// terminates naturally.
//
// Replaces the prior blocking Read-style API so callers can select on
// this channel together with a context — required for cancel-safe
// detach in the WS takeover path.
func (s *Session) Output() <-chan []byte { return s.outCh }

// pump is the single PTY reader. Runs until the master fd reports EOF
// (i.e., bash has exited or Close has closed the fd), at which point
// the output channel is closed so range loops exit.
func (s *Session) pump() {
	defer close(s.outCh)
	buf := make([]byte, 4096)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			s.outCh <- chunk
		}
		if err != nil {
			return
		}
	}
}

// Write forwards bytes to bash's stdin. Returns the number of bytes
// written. After Close, returns 0 + io.ErrClosedPipe.
func (s *Session) Write(p []byte) (int, error) {
	if s.closed.Load() {
		return 0, io.ErrClosedPipe
	}
	return s.ptmx.Write(p)
}

// Resize updates the PTY's window size. Safe to call repeatedly as the
// pane is dragged. Both dimensions must be > 0.
func (s *Session) Resize(cols, rows uint16) error {
	if cols == 0 || rows == 0 {
		return fmt.Errorf("shelldriver: cols and rows must be > 0 (got %dx%d)", cols, rows)
	}
	if s.closed.Load() {
		return io.ErrClosedPipe
	}
	return pty.Setsize(s.ptmx, &pty.Winsize{Cols: cols, Rows: rows})
}

// Done returns a channel closed when the spawned process has fully
// exited. Tests rely on this for deterministic teardown.
func (s *Session) Done() <-chan struct{} { return s.doneCh }

// Close terminates the bash process group: SIGTERM first, then SIGKILL
// after a short grace period if it hasn't exited. Idempotent — repeat
// calls are no-ops. Returns the bash process's exit error, if any (a
// non-zero exit from bash isn't itself an error; only a startup-time
// or signaling error is reported).
func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		// Signal the process group so a child the user spawned
		// (vim, htop) goes down with bash.
		if s.cmd != nil && s.cmd.Process != nil {
			pgid, err := syscall.Getpgid(s.pid)
			if err == nil {
				_ = syscall.Kill(-pgid, syscall.SIGTERM)
			} else {
				_ = s.cmd.Process.Signal(syscall.SIGTERM)
			}
		}
		select {
		case <-s.doneCh:
		case <-time.After(500 * time.Millisecond):
			// Escalate. The bash process should respect SIGTERM
			// promptly; this guards against a hung subprocess.
			if s.cmd != nil && s.cmd.Process != nil {
				pgid, err := syscall.Getpgid(s.pid)
				if err == nil {
					_ = syscall.Kill(-pgid, syscall.SIGKILL)
				} else {
					_ = s.cmd.Process.Kill()
				}
			}
			<-s.doneCh
		}
		// Closing the master after the child has exited unblocks any
		// in-flight Output reads with io.EOF.
		_ = s.ptmx.Close()
	})
	return s.exitErr
}

// reap waits for the bash process to exit and closes doneCh. Runs in
// its own goroutine so Close can race against it on the timeout.
func (s *Session) reap() {
	if s.cmd != nil {
		s.exitErr = s.cmd.Wait()
	}
	close(s.doneCh)
}

// resolveCwd picks the bash starting directory in priority order:
// caller's choice (if a valid absolute dir), $HOME, then the gridwell
// process's own cwd. A non-existent path is rejected so bash doesn't
// die at exec with a misleading "no such file or directory".
func resolveCwd(want string) string {
	if want != "" && dirExists(want) {
		return want
	}
	if h := os.Getenv("HOME"); h != "" && dirExists(h) {
		return h
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return "/"
}

func dirExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

