// Package caps owns the client's environment capability set: what the host can
// place over the page — native live URL views and native menus — and whether
// this node has shells. It is derived once at boot, so no other code tests for
// the bridge to make a feature decision, and, like client/pluginhealth, it
// holds the errsurface report for a missing capability.
package caps

import "github.com/josephburnett/gridwell/client/errsurface"

// Caps is immutable once derived.
type Caps struct {
	LiveURL bool
	// ChoiceMenu is whether the host renders a menu of declared choices for
	// the client. Without it the client draws its own DOM popover.
	ChoiceMenu bool
	// Shells is whether shell tiles exist on this node at all: the + palette
	// offers the primitive, a descent attaches, a create is accepted.
	Shells bool
}

// Bridge is what the native host declares it can do, window.gridwell's caps
// field. Each half is declared on its own, so a host can place live url views
// without the rest of the desktop and a bridge that declares nothing is a
// plain browser. Shells are not on the list: the PTY rides the web door.
type Bridge struct {
	// LiveURL is whether the host implements placeWebview and setBounds.
	LiveURL bool
	// ChoiceMenu is whether the host implements showChoiceMenu.
	ChoiceMenu bool
}

// NoBridge is a plain browser host.
func NoBridge() Bridge { return Bridge{} }

// Derive runs once before the handshake with shellsDisabled unknown and again
// when that fact lands, and not after.
func Derive(bridge Bridge, shellsDisabled bool) Caps {
	return Caps{
		LiveURL:    bridge.LiveURL,
		ChoiceMenu: bridge.ChoiceMenu,
		Shells:     !shellsDisabled,
	}
}

// GoLiveNotice reports a gesture that asked for a live view without LiveURL. A
// missing capability is expected, so Info, and one source coalesces taps.
func GoLiveNotice() (sev errsurface.Severity, source, message string) {
	return errsurface.Info, "livecap", "live web views need the desktop app — showing the frozen preview"
}

// ShellNotice is GoLiveNotice for a shell tile, on the same source.
func ShellNotice() (sev errsurface.Severity, source, message string) {
	return errsurface.Info, "livecap", "this node has shells turned off — showing the frozen preview"
}
