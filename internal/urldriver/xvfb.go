//go:build !js

package urldriver

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Xvfb is a managed Xvfb subprocess. Gridwell spawns one at startup so that
// headful Chromium has a display to render into without popping a real window
// on the user's desktop.
//
// Listening: TCP only (port 6000 + display number). Xvfb also tries to create
// a Unix socket under /tmp/.X11-unix, but under WSLg that directory is a
// read-only bind mount; we pass `-pn` so Xvfb tolerates the Unix-socket
// failure and we point clients at the TCP transport instead.
type Xvfb struct {
	cmd     *exec.Cmd
	display string // "127.0.0.1:99"
}

const (
	xvfbDisplay   = 99
	xvfbTCPPort   = 6000 + xvfbDisplay
	xvfbStartWait = 3 * time.Second
)

// StartXvfb spawns Xvfb at display :99 with the given screen resolution.
// Returns ErrXvfbMissing if the Xvfb binary isn't installed.
func StartXvfb(width, height int) (*Xvfb, error) {
	bin, err := exec.LookPath("Xvfb")
	if err != nil {
		return nil, ErrXvfbMissing
	}

	// If something is already listening on the TCP port, refuse rather
	// than silently colliding with another Gridwell or X server.
	if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", xvfbTCPPort), 200*time.Millisecond); err == nil {
		_ = c.Close()
		return nil, fmt.Errorf("%w: TCP port %d already in use", ErrXvfbInUse, xvfbTCPPort)
	}

	cmd := exec.Command(bin,
		fmt.Sprintf(":%d", xvfbDisplay),
		"-screen", "0", fmt.Sprintf("%dx%dx24", width, height),
		// Enable TCP listening so clients can reach us at
		// 127.0.0.1:99 even when the Unix socket can't be created.
		"-listen", "tcp",
		// Accept partial-listen failure (some transports unable to
		// bind) as long as at least one transport succeeded. Under
		// WSLg /tmp/.X11-unix is read-only and would otherwise abort
		// Xvfb's initialization.
		"-pn",
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGTERM,
	}
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start Xvfb: %w", err)
	}

	// Poll the TCP socket for readiness. Xvfb opens the port within tens
	// of milliseconds on a healthy box; we give it up to xvfbStartWait.
	deadline := time.Now().Add(xvfbStartWait)
	for time.Now().Before(deadline) {
		if cmd.ProcessState != nil {
			return nil, fmt.Errorf("Xvfb exited early: %v", cmd.ProcessState)
		}
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", xvfbTCPPort), 100*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return &Xvfb{
				cmd:     cmd,
				display: fmt.Sprintf("127.0.0.1:%d", xvfbDisplay),
			}, nil
		}
		time.Sleep(50 * time.Millisecond)
	}

	_ = cmd.Process.Signal(syscall.SIGTERM)
	return nil, fmt.Errorf("Xvfb did not start listening on 127.0.0.1:%d within %s", xvfbTCPPort, xvfbStartWait)
}

// Display returns the X11 display string ("DISPLAY" env value) for this
// Xvfb instance. Clients should use this verbatim; it points at the TCP
// transport so it works even when the Unix socket dir is read-only.
func (x *Xvfb) Display() string { return x.display }

// Stop terminates the Xvfb process and its group.
func (x *Xvfb) Stop() {
	if x == nil || x.cmd == nil || x.cmd.Process == nil {
		return
	}
	pgid, err := syscall.Getpgid(x.cmd.Process.Pid)
	if err == nil {
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
	} else {
		_ = x.cmd.Process.Signal(syscall.SIGTERM)
	}
	_, _ = x.cmd.Process.Wait()
}

// ErrXvfbMissing indicates the Xvfb binary isn't installed.
var ErrXvfbMissing = errors.New("Xvfb not installed (apt install xvfb)")

// ErrXvfbInUse indicates the TCP port is taken (another X server / leftover Xvfb).
var ErrXvfbInUse = errors.New("Xvfb display already in use")
