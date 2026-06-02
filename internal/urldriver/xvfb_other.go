//go:build !linux && !js

package urldriver

import "errors"

// Xvfb is a stub on non-Linux platforms. Gridwell relies on Xvfb to give
// headful Chromium a display server; outside Linux there is no equivalent
// to manage from Go, so callers should pass --no-xvfb and use --headless
// instead. The type exists so package-level signatures stay portable.
type Xvfb struct{}

// StartXvfb is unsupported on non-Linux platforms. The serve command will
// auto-default --no-xvfb on macOS / *BSD; this path is here for callers
// that pass --no-xvfb=false explicitly so they get a clear error.
func StartXvfb(width, height int) (*Xvfb, error) {
	return nil, ErrXvfbUnsupported
}

// Display returns the empty string; the stub never starts an X server.
func (x *Xvfb) Display() string { return "" }

// Stop is a no-op on non-Linux platforms.
func (x *Xvfb) Stop() {}

// ErrXvfbMissing keeps signature parity with the Linux build. Not returned
// by the stub but referenced by callers that probe for Xvfb installation.
var ErrXvfbMissing = errors.New("Xvfb not available on this platform")

// ErrXvfbInUse keeps signature parity with the Linux build.
var ErrXvfbInUse = errors.New("Xvfb not available on this platform")

// ErrXvfbUnsupported indicates --no-xvfb is required: this build has no
// Xvfb integration. Pass --no-xvfb (and --headless) on macOS / *BSD.
var ErrXvfbUnsupported = errors.New("Xvfb is Linux-only; pass --no-xvfb (and --headless) on this platform")
