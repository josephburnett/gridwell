// Package shellconn holds the decisions the wasm shell attachment makes: what
// a descent of any kind does about liveness, whether the refresh button
// shows, who owns a link press, and how a freeze capture decodes. Stream
// lifecycle is client/shellstream, over the dialer in client/shellws.
package shellconn

import "encoding/base64"

// DecodeJPEGDataURL decodes in Go rather than through JS atob, whose binary
// string re-encodes as UTF-8 through js.Value.String() and doubles every byte
// at or above 0x80.
func DecodeJPEGDataURL(s string) ([]byte, bool) {
	const prefix = "data:image/jpeg;base64,"
	if len(s) <= len(prefix) || s[:len(prefix)] != prefix {
		return nil, false
	}
	out, err := base64.StdEncoding.DecodeString(s[len(prefix):])
	if err != nil {
		return nil, false
	}
	return out, true
}

// AutoLive is what a descent does about liveness. Descending is the
// engagement gesture, so it reconnects the shell and reopens the url.
type AutoLive int

const (
	// AutoLiveNone descends silently: a notice belongs to an explicit
	// gesture.
	AutoLiveNone AutoLive = iota
	AutoLiveURL
	// AutoLiveShell attaches to a live session, or creates one for a tile
	// that has never been opened.
	AutoLiveShell
	// AutoLiveProbeShell probes the session first and decides again.
	AutoLiveProbeShell
)

// DecideAutoLive reads the same aliveness facts DecideShellRefreshVisible
// does, so the two agree about what a dead session means. frozen is the user's
// standing freeze, one arm for every kind: it beats the engagement default
// until the reconnect gesture clears it (client/golive).
func DecideAutoLive(webContent, kindShell, liveURL, liveShell, hasPreview, aliveKnown, alive, frozen bool) AutoLive {
	if frozen {
		return AutoLiveNone
	}
	switch {
	case webContent:
		if liveURL {
			return AutoLiveURL
		}
	case kindShell:
		if !liveShell {
			return AutoLiveNone
		}
		if !hasPreview {
			return AutoLiveShell // fresh tile: create, as the create path does
		}
		if !aliveKnown {
			return AutoLiveProbeShell
		}
		if alive {
			return AutoLiveShell
		}
	}
	return AutoLiveNone
}

type RefreshVisibility struct {
	Show  bool
	Probe bool
}

// DecideShellRefreshVisible: a tile with no preview blob has never been
// opened, so refresh creates a session and always shows; a session cached dead
// has no recovery and hides.
func DecideShellRefreshVisible(isShell, hasPreview, aliveKnown, alive bool) RefreshVisibility {
	if !isShell {
		return RefreshVisibility{}
	}
	if !hasPreview {
		return RefreshVisibility{Show: true}
	}
	if aliveKnown {
		return RefreshVisibility{Show: alive}
	}
	return RefreshVisibility{Probe: true}
}

// MouseTrackingNone is xterm's modes.mouseTrackingMode for an application not
// tracking the mouse. An empty string, a terminal that did not answer, reads
// the same, so DecideLinkPress swallows only a press it is sure about.
const MouseTrackingNone = "none"

// DecideLinkPress gives Gridwell a press over a hovered link while the
// application is tracking the mouse, because xterm both activates the link and
// reports the press and an application with its own opener would open the url
// again in the host browser. Two presses stay the terminal's: with nothing
// tracking the press is xterm's selection start, and a held modifier is the
// escape hatch from a tracking application.
func DecideLinkPress(hoveredURL, mouseTracking string, modifier bool) bool {
	if hoveredURL == "" || modifier {
		return false
	}
	return mouseTracking != "" && mouseTracking != MouseTrackingNone
}

// ExitAlive is what a stream's end says about the session's liveness, the
// fact DecideAutoLive and DecideShellRefreshVisible read back. sessionGone is
// the server's definitive verdict, so the answer is known dead; any other
// end carries none, so the cached answer is forgotten and the next descent
// probes again.
func ExitAlive(sessionGone bool) (alive, known bool) {
	if sessionGone {
		return false, true
	}
	return false, false
}
