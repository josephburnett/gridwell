// Package shellconn holds the pure decision logic for the shell-stream
// WebSocket lifecycle, factored out of the wasm client so it is testable
// without a browser or a live socket.
package shellconn

import "encoding/base64"

// WSSendAction is what to do with an outgoing shell frame given the
// WebSocket's current readyState.
type WSSendAction int

const (
	// WSDrop: socket is CLOSING/CLOSED (or an unknown state) — drop the frame.
	WSDrop WSSendAction = iota
	// WSQueue: socket is CONNECTING — hold the frame until it opens.
	WSQueue
	// WSSend: socket is OPEN — send now.
	WSSend
)

// ShellSendAction maps a WebSocket readyState to whether an outgoing frame
// should be queued (CONNECTING = 0), sent (OPEN = 1), or dropped (CLOSING =
// 2 / CLOSED = 3, or anything else). Dropping a keystroke while CONNECTING —
// or trying to send on a closed socket — is the hazard this pins down; both
// the binary and text send paths route through it so they can't diverge.
func ShellSendAction(readyState int) WSSendAction {
	switch readyState {
	case 0:
		return WSQueue
	case 1:
		return WSSend
	default:
		return WSDrop
	}
}

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

// closePolicyViolation is the WebSocket close code (RFC 6455 1008,
// PolicyViolation) the shell-stream server sends when wasm asked to attach
// but the tmux session no longer exists — the one definitive "session gone"
// signal the client gets over the socket.
const closePolicyViolation = 1008

// SessionDeadOnClose reports whether a shell-stream WebSocket close with the
// given code is a *definitive* "the tmux session is gone" signal.
//
// Only 1008 (PolicyViolation) qualifies. Every other close code — a clean
// teardown (1000), an abnormal closure (1006: handshake/attach failed, server
// died, network dropped), a server error (1011), or a missing code — carries
// no application-level guarantee that the session is alive, so the client must
// treat liveness as UNKNOWN and re-probe the server rather than caching
// alive=true. Caching alive=true on a non-1008 close was the bug: a 1006 from
// a failed attach against a dead session left the refresh button showing and
// short-circuited the authoritative ShellSessionAlive probe.
func SessionDeadOnClose(code int) bool {
	return code == closePolicyViolation
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
