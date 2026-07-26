// Package caps owns the client's environment capability set — the single
// answer to "what can this shell do." The same wasm client runs under two
// hosts: the Electron desktop app (whose preload exposes the window.gridwell
// bridge) and a plain browser (an iPhone pointed at the Go server's origin).
// The capability difference between them is derived exactly once at boot and
// read everywhere; no other code asks "is there a bridge" to make a feature
// decision.
//
// Mirrors client/pluginhealth: a pure classification plus the ready-made
// errsurface report for the gesture that hits the missing capability, so a
// tap on an unavailable affordance explains itself instead of dying silently
// (charter §6).
package caps

import "github.com/josephburnett/gridwell/client/errsurface"

// Caps is the capability set for this client instance. Derived once at boot;
// immutable after.
type Caps struct {
	// LiveURL: a URL tile can go live as a native browser view. Only the
	// Electron shell can host one (a WebContentsView over the canvas); a
	// plain browser shows the frozen preview instead.
	LiveURL bool
	// LiveShell: a shell tile can attach its live PTY. The bytes ride the
	// Electron main process's gRPC OpenShell stream over IPC (owner decision
	// 2026-07-26, reversing the browser-client "shells stay live" call: the
	// WS bridge is gone, and a plain browser shows the frozen preview like a
	// url tile).
	LiveShell bool
}

// Derive computes the capability set from the one environmental fact that
// distinguishes the hosts: whether the Electron preload bridge
// (window.gridwell) is present.
func Derive(bridgePresent bool) Caps {
	return Caps{LiveURL: bridgePresent, LiveShell: bridgePresent}
}

// GoLiveNotice returns the errsurface report for a gesture that asked a URL
// tile to go live when LiveURL is false — the tap on the slashed corner
// button, or a visit gesture that can only end frozen. Info severity: a
// missing capability is an expected property of this host, not a failure.
// The stable source coalesces repeated taps into one row.
func GoLiveNotice() (sev errsurface.Severity, source, message string) {
	return errsurface.Info, "livecap", "live web views need the desktop app — showing the frozen preview"
}

// ShellNotice returns the errsurface report for a gesture that asked a shell
// tile to attach when LiveShell is false. Same shape and severity rationale
// as GoLiveNotice; same stable source, so mixed taps still coalesce into one
// capability row.
func ShellNotice() (sev errsurface.Severity, source, message string) {
	return errsurface.Info, "livecap", "live shells need the desktop app — showing the frozen preview"
}
