//go:build !js

package urldriver

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// Xvfb is a managed Xvfb subprocess. Gridwell spawns one at startup so that
// headful Chromium has a display to render into without popping a real window
// on the user's desktop.
type Xvfb struct {
	cmd     *exec.Cmd
	display string // ":99"
}

// StartXvfb spawns Xvfb at display :99 with the given screen resolution.
// Returns ErrXvfbInUse if the display is already taken and ErrXvfbMissing if
// the Xvfb binary isn't installed.
func StartXvfb(width, height int) (*Xvfb, error) {
	const display = ":99"

	// Probe whether :99 is already in use. Xvfb communicates readiness via
	// the lock file /tmp/.X99-lock and the socket /tmp/.X11-unix/X99.
	if _, err := os.Stat(filepath.Join("/tmp/.X11-unix", "X99")); err == nil {
		return nil, fmt.Errorf("%w: display %s in use", ErrXvfbInUse, display)
	}

	bin, err := exec.LookPath("Xvfb")
	if err != nil {
		return nil, ErrXvfbMissing
	}

	cmd := exec.Command(bin, display,
		"-screen", "0", fmt.Sprintf("%dx%dx24", width, height),
		"-nolisten", "tcp")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		// Set process group so we can kill the whole tree on Stop.
		Setpgid: true,
		// Kill Xvfb when gridwell dies (Linux-only; safe in WSL2).
		Pdeathsig: syscall.SIGTERM,
	}
	// Drop Xvfb's chatter; on launch failure we still get an exit status.
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start Xvfb: %w", err)
	}

	// Poll for the socket file to appear, with a short timeout. Xvfb is
	// fast on a healthy system; ~2s is generous.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join("/tmp/.X11-unix", "X99")); err == nil {
			return &Xvfb{cmd: cmd, display: display}, nil
		}
		// If the process died before becoming ready, fail fast.
		if cmd.ProcessState != nil {
			return nil, fmt.Errorf("Xvfb exited early: %v", cmd.ProcessState)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Timeout — try to clean up the half-started process.
	_ = cmd.Process.Signal(syscall.SIGTERM)
	return nil, fmt.Errorf("Xvfb did not become ready within 2s")
}

// Display returns the X11 display string ("DISPLAY" env value) for this
// Xvfb instance.
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

// ErrXvfbInUse indicates another X server is already on :99.
var ErrXvfbInUse = errors.New("Xvfb display already in use")
