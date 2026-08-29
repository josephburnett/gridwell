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
	// Shells: shell tiles exist on this node at all. False when the server
	// declares shells_disabled (server.yaml disable_shells): the + palette
	// offers no shell primitive and the server refuses shell creates and
	// PTY attaches regardless of what any client asks.
	Shells bool
}

// Bridge is what the native host DECLARES it can do (2026-08-13): the
// window.gridwell object's own caps field, read once at boot. A host that
// exposes the bridge no longer implies every native feature — a mobile
// shell can place live url views without carrying the shell PTY relay.
// A legacy bridge with no caps field (an older Electron preload under a
// newer wasm client) declares both — exactly what that host supports.
type Bridge struct {
	// Present: window.gridwell exists at all (false = plain browser).
	Present bool
	// LiveURL / LiveShell: the host implements the url-view half
	// (placeWebview/setBounds/…) / the shell relay half (shellOpen/…).
	LiveURL   bool
	LiveShell bool
}

// LegacyBridge is the declaration imputed to a bridge without a caps
// field: the full Electron feature set, which is the only host that shape
// ever shipped in.
func LegacyBridge() Bridge {
	return Bridge{Present: true, LiveURL: true, LiveShell: true}
}

// NoBridge is a plain browser host.
func NoBridge() Bridge { return Bridge{} }

// Derive computes the capability set from the boot facts: what the native
// bridge declares (window.gridwell.caps), and whether the node disabled
// shells (the handshake's shells_disabled). Called once with
// the node fact unknown before the handshake and re-derived once when it
// lands — still strictly boot-time, immutable afterward.
func Derive(bridge Bridge, shellsDisabled bool) Caps {
	return Caps{
		LiveURL:   bridge.Present && bridge.LiveURL,
		LiveShell: bridge.Present && bridge.LiveShell && !shellsDisabled,
		Shells:    !shellsDisabled,
	}
}

// GoLiveNotice returns the errsurface report for a gesture that asked a URL
// tile to go live when LiveURL is false — the ephemeral-visit gesture,
// which can only end frozen. (The bar slot no longer posts this: on a
// browser host it opens the address in a new tab instead — 2026-08-09.)
// Info severity: a missing capability is an expected property of this
// host, not a failure. The stable source coalesces repeated taps into one
// row.
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
