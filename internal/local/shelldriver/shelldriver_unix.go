//go:build unix

// The real driver, over creack/pty.
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

// outputBufferFrames is deep enough that a takeover's detach-to-reattach gap
// drops no output. When it is full the pump blocks on the PTY read, which
// back-pressures the process; dropping bytes could truncate an escape sequence.
const outputBufferFrames = 64

// sigtermGrace is what Close gives the process group to honor SIGTERM before
// it sends SIGKILL, for a subprocess that hangs instead of respecting it. A
// var so a test can wait it out; see shelldriver_unix_test.go.
var sigtermGrace = 500 * time.Millisecond

// Session is one live PTY. Every method is safe to call concurrently, and one
// that needs the PTY after Close returns an error rather than panicking on a
// torn-down descriptor.
type Session struct {
	cmd  *exec.Cmd
	ptmx *os.File
	pid  int

	// outCh is the single drain point for PTY bytes, one pump goroutine in
	// and one subscriber out. The takeover protocol needs a cancel-safe
	// select, which a blocking PTY Read could not satisfy.
	outCh chan []byte

	closeOnce sync.Once
	closed    atomic.Bool
	doneCh    chan struct{}
	exitErr   error
}

// Start returns as soon as exec succeeds and the PTY is ready.
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
		// What xterm.js claims on the client, so prompt rendering is not
		// flat.
		env := append([]string{}, os.Environ()...)
		if os.Getenv("TERM") == "" {
			env = append(env, "TERM=xterm-256color")
		}
		cmd.Env = env
	}
	// A fresh process group, so the parent terminal's SIGINT and SIGTSTP are
	// not forwarded. Close does the signalling instead.
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

// Output's chunks are fresh slices owned by the receiver, and the channel
// closes once the process exits or Close runs. After Close, chunks the PTY
// produced before the fd closed may still arrive first, the pump's cancellable
// send racing the consumer's receive, so consumers must drain to close.
func (s *Session) Output() <-chan []byte { return s.outCh }

// pump is the single PTY reader, running until the master fd reports EOF.
func (s *Session) pump() {
	defer close(s.outCh)
	buf := make([]byte, 4096)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			// A plain send would wedge here forever when outCh is full and
			// nobody drains it, because closing the PTY unblocks a blocked
			// Read and leaves a blocked send alone. doneCh closes when the
			// process exits, so the final chunk is dropped instead of leaking
			// the goroutine and the fd. While the process lives doneCh is
			// open, so a full channel still back-pressures the read.
			select {
			case s.outCh <- chunk:
			case <-s.doneCh:
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// Write returns 0 and io.ErrClosedPipe after Close.
func (s *Session) Write(p []byte) (int, error) {
	if s.closed.Load() {
		return 0, io.ErrClosedPipe
	}
	return s.ptmx.Write(p)
}

// Resize needs both dimensions > 0 and is safe to call on every drag frame.
func (s *Session) Resize(cols, rows uint16) error {
	if cols == 0 || rows == 0 {
		return fmt.Errorf("shelldriver: cols and rows must be > 0 (got %dx%d)", cols, rows)
	}
	if s.closed.Load() {
		return io.ErrClosedPipe
	}
	return pty.Setsize(s.ptmx, &pty.Winsize{Cols: cols, Rows: rows})
}

// Done returns a channel closed when the spawned process has fully exited.
func (s *Session) Done() <-chan struct{} { return s.doneCh }

// Close sends SIGTERM, then SIGKILL after sigtermGrace. Repeat calls are
// no-ops. The error it returns is the process's exit status, not a teardown
// failure.
func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		// The process group, so a child the user spawned goes down too.
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
		case <-time.After(sigtermGrace):
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
		// Closing the master unblocks any in-flight Output read with io.EOF.
		_ = s.ptmx.Close()
	})
	return s.exitErr
}

// reap runs in its own goroutine so Close can race it on the timeout.
func (s *Session) reap() {
	if s.cmd != nil {
		s.exitErr = s.cmd.Wait()
	}
	close(s.doneCh)
}

// resolveCwd takes the caller's choice if it is an existing dir, then $HOME,
// then this process's cwd. A path that does not exist is rejected here so bash
// does not die at exec with a misleading "no such file or directory".
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
