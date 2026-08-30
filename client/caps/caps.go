// Package caps owns the client's environment capability set — the single
// answer to "what can this shell do." The same wasm client runs under two
// hosts: the Electron desktop app (whose preload exposes the window.gridwell
// bridge) and a plain browser (an iPhone pointed at the Go server's origin).
// The capability difference between them is derived exactly once at boot and
// read everywhere; no other code asks "is there a bridge" to make a feature
// decision. That difference is one thing: native live URL views. Shells ride
// the web door on every host.
//
// Mirrors client/pluginhealth: a pure classification plus the ready-made
// errsurface report for the gesture that hits the missing capability, so a
// tap on an unavailable affordance explains itself instead of dying
// silently.
package caps

import "github.com/josephburnett/gridwell/client/errsurface"

// Caps is the capability set for this client instance. Derived once at boot;
// immutable after.
type Caps struct {
	// LiveURL: a URL tile can go live as a native browser view. Only the
	// Electron shell can host one (a WebContentsView over the canvas); a
	// plain browser shows the frozen preview instead.
	LiveURL bool
	// LiveShell: a shell tile can attach its live PTY. True wherever this
	// client runs: the PTY rides a WebSocket on the web door — the page's
	// own origin, the page's own cookie — so a browser attaches exactly as
	// the desktop does. The only thing that turns it off is the node
	// refusing shells (server.yaml disable_shells), which is not a host
	// capability at all.
	LiveShell bool
	// Shells: shell tiles exist on this node at all. False when the server
	// declares shells_disabled (server.yaml disable_shells): the + palette
	// offers no shell primitive and the server refuses shell creates and
	// PTY attaches regardless of what any client asks.
	Shells bool
}

// Bridge is what the native host declares it can do: the window.gridwell
// object's own caps field, read once at boot. Exposing the bridge does not
// imply every native feature — a mobile shell can place live url views
// without carrying the whole desktop's machinery. Shells are not on this
// list; the PTY rides the web door, so no host implements anything for it.
type Bridge struct {
	// Present: window.gridwell exists at all (false = plain browser).
	Present bool
	// LiveURL: the host implements the url-view half (placeWebview/
	// setBounds/…).
	LiveURL bool
}

// LegacyBridge is the declaration imputed to a bridge without a caps
// field: the full Electron feature set.
func LegacyBridge() Bridge {
	return Bridge{Present: true, LiveURL: true}
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
		LiveShell: !shellsDisabled,
		Shells:    !shellsDisabled,
	}
}

// GoLiveNotice returns the errsurface report for a gesture that asked a URL
// tile to go live when LiveURL is false — the ephemeral-visit gesture, which
// can only end frozen. Info severity: a missing capability is an expected
// property of this host, not a failure. The stable source coalesces repeated
// taps into one row.
func GoLiveNotice() (sev errsurface.Severity, source, message string) {
	return errsurface.Info, "livecap", "live web views need the desktop app — showing the frozen preview"
}

// ShellNotice returns the errsurface report for a gesture that asked a shell
// tile to attach when LiveShell is false, which means one thing: the node
// refuses shells (server.yaml disable_shells). Same shape and severity
// rationale as GoLiveNotice; same stable source, so mixed taps coalesce into
// one capability row.
func ShellNotice() (sev errsurface.Severity, source, message string) {
	return errsurface.Info, "livecap", "this node has shells turned off — showing the frozen preview"
}
