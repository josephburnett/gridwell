// Package shellconn holds the pure decision logic for the shell-stream
// WebSocket lifecycle, factored out of the wasm client so it is testable
// without a browser or a live socket.
package shellconn

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
