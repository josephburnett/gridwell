// Package shellconn holds the pure decision logic for the wasm shell
// attachment (refresh-button visibility, freeze-capture decoding), factored
// out of the wasm client so it is testable without a browser. Stream
// LIFECYCLE is not here: it lives in client/shellstream, over the /shell
// WebSocket dialer in client/shellws (2026-08-29).
package shellconn

import "encoding/base64"

// DecodeJPEGDataURL decodes a "data:image/jpeg;base64,..." data URL into the
// raw JPEG bytes. ok is false when s lacks the exact prefix or the base64
// body doesn't decode.
//
// The decode is done in Go on purpose, NOT via JS atob: atob returns a binary
// string whose code units are byte values, but reading it back through
// js.Value.String() re-encodes as UTF-8 and doubles every byte ≥ 0x80,
// corrupting the JPEG (FF D8 … becomes C3 BF C3 98 …).
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

// AutoLive is the verdict for what a DESCENT into a tile does about
// liveness (owner decision 2026-07-26, issue #202: descending IS the
// engagement gesture — reconnect the shell, reopen the URL; the frozen
// preview is what a tile looks like from OUTSIDE).
type AutoLive int

const (
	// AutoLiveNone: stay frozen — a text tile, a capability-gated host (a
	// browser descends silently frozen into a URL tile; notices are for
	// explicit gestures), or a shell whose session is known dead (nothing to
	// reconnect; the refresh affordance is hidden for it too).
	AutoLiveNone AutoLive = iota
	// AutoLiveURL: open the native url view.
	AutoLiveURL
	// AutoLiveShell: open the PTY stream — attach to the live session, or
	// create fresh for a never-opened tile (no preview blob), matching what
	// the create-path descent always did.
	AutoLiveShell
	// AutoLiveProbeShell: the session's aliveness is unknown — probe first,
	// then re-decide from the verdict (alive → open; dead → stay frozen).
	AutoLiveProbeShell
)

// DecideAutoLive maps a descent's facts to its liveness action. webContent
// is rpc.Tile.WebContent() — the ONE classification of "presents as web
// content" (a url tile or a serves_page tile; the two must engage
// identically), fed by the caller so this package never re-derives it;
// kindShell classifies the shell arm; liveURL/liveShell are the host
// capabilities; hasPreview and the aliveness pair are the shell tile's
// state (same inputs as DecideShellRefreshVisible — the two decisions must
// agree about what a dead session means, so they read the same facts).
// urlFrozen is the user's standing freeze on a url tile (issue #237): a
// deliberate freeze beats the engagement default, so re-descending stays
// frozen until the reconnect gesture clears the stored intent (page tiles
// carry no standing freeze; the input is always false for them).
func DecideAutoLive(webContent, kindShell, liveURL, liveShell, hasPreview, aliveKnown, alive, urlFrozen bool) AutoLive {
	switch {
	case webContent:
		if liveURL && !urlFrozen {
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

// RefreshVisibility is the verdict for a frozen shell descent's refresh
// button: whether it shows, and whether the caller must kick off a
// liveness probe (because the tmux session's state is unknown).
type RefreshVisibility struct {
	Show  bool
	Probe bool
}

// DecideShellRefreshVisible decides whether the lower-right refresh
// button paints on a frozen shell descent, and whether a
// ShellSessionAlive probe must be started. Pure: the caller resolves the
// inputs (and performs the probe side effect when Probe is set).
//
//   - not a shell tile        → hidden, no probe.
//   - no preview blob yet      → fresh tile; refresh creates a new tmux
//     session, so always show.
//   - liveness cached alive    → refresh attaches; show.
//   - liveness cached dead      → no recovery possible; hide.
//   - liveness unknown          → hide and probe; a redraw follows when
//     the probe result lands.
//
// aliveKnown is whether the liveness probe result is cached at all;
// alive is that cached value (meaningful only when aliveKnown).
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
