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
