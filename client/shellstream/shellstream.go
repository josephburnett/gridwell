// Package shellstream owns the lifecycle of the client's live shell
// attachments: which pane holds which stream, what a replacement does to the
// one it replaced, and when the renderer is told a stream ended. The PTY
// rides a WebSocket on the web door (client/shellwire); this package is
// js-free and unit-tested, and the wasm shim is glue that hands over a dialer
// and two callbacks.
//
// The rules:
//
//   - Open for a pane replaces that pane's existing stream (close first).
//   - Write and Resize after Close, or before Open, are silent no-ops: a race
//     between a teardown and an in-flight keystroke is expected, not an
//     error.
//   - An end fires at most once per stream, and only while that stream is
//     still the pane's current one. A local Close or a replacement suppresses
//     it — the caller initiated those and already knows. Without the
//     suppression, a replaced stream's late end would freeze the pane right
//     after its new stream attached.
//   - Output routes through the registry, not the closure: a replaced
//     stream's late bytes must never reach the renderer as the new stream's
//     output.
package shellstream

import "sync"

// Handle is the write side of one live attachment, as a Dialer returns it.
type Handle interface {
	Write(data []byte)
	Resize(cols, rows int)
	// Close ends the attachment from this side. The dialer must still
	// deliver onEnd exactly once afterwards.
	Close()
}

// Dialer opens one attachment bound to tileID. onData delivers PTY output;
// onEnd delivers the end exactly once — message "" for a clean end, else
// the failure text; sessionGone marks the server's definitive "this session
// no longer exists" verdict, which the caller reads to hide the refresh
// affordance.
type Dialer func(tileID string, cols, rows int, onData func(data []byte), onEnd func(message string, sessionGone bool)) Handle

// Exit is an unexpected end reported to the caller: the pane whose terminal
// died, why, and whether the session is gone for good.
type Exit struct {
	PaneID      string
	Message     string
	SessionGone bool
}

type entry struct {
	handle Handle
	ended  bool
}

// Registry is the per-pane map of live attachments.
type Registry struct {
	dial   Dialer
	onData func(paneID string, data []byte)
	onExit func(Exit)

	mu      sync.Mutex
	streams map[string]*entry
}

// New wires a registry to its dialer and the two renderer callbacks. Both
// callbacks run outside the registry's lock, so a callback may call back in —
// a freeze that closes the pane, say — without deadlocking.
func New(dial Dialer, onData func(paneID string, data []byte), onExit func(Exit)) *Registry {
	return &Registry{dial: dial, onData: onData, onExit: onExit, streams: map[string]*entry{}}
}

// Open attaches paneID to tileID's PTY, replacing whatever that pane held.
func (r *Registry) Open(paneID, tileID string, cols, rows int) {
	r.Close(paneID)
	// The slot is claimed before the dial: a dialer that fails instantly
	// calls onEnd synchronously, and an unclaimed slot would swallow that
	// report, leaving the pane forever on a stream that never opened.
	e := &entry{}
	r.mu.Lock()
	r.streams[paneID] = e
	r.mu.Unlock()
	handle := r.dial(
		tileID, cols, rows,
		func(data []byte) {
			if !r.current(paneID, e) {
				return
			}
			r.onData(paneID, data)
		},
		func(message string, sessionGone bool) {
			r.mu.Lock()
			if e.ended { // exactly-once, whatever raced
				r.mu.Unlock()
				return
			}
			e.ended = true
			if r.streams[paneID] != e { // replaced/closed: the caller already knows
				r.mu.Unlock()
				return
			}
			delete(r.streams, paneID)
			r.mu.Unlock()
			r.onExit(Exit{PaneID: paneID, Message: message, SessionGone: sessionGone})
		},
	)
	r.mu.Lock()
	e.handle = handle
	ended := e.ended
	r.mu.Unlock()
	if ended {
		// The dial failed instantly (a bad address, a refused upgrade):
		// onEnd already reported it and nothing holds this handle now.
		handle.Close()
	}
}

func (r *Registry) current(paneID string, e *entry) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.streams[paneID] == e
}

// Write forwards keystroke bytes to the pane's PTY. No-op for a pane with
// no live stream.
func (r *Registry) Write(paneID string, data []byte) {
	if h := r.handle(paneID); h != nil {
		h.Write(data)
	}
}

// Resize forwards a winsize change. No-op for a pane with no live stream.
func (r *Registry) Resize(paneID string, cols, rows int) {
	if h := r.handle(paneID); h != nil {
		h.Resize(cols, rows)
	}
}

func (r *Registry) handle(paneID string) Handle {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.streams[paneID]; ok {
		return e.handle
	}
	return nil
}

// Close ends the pane's attachment from this side. The exit report is
// suppressed: this side asked, and does its own teardown.
func (r *Registry) Close(paneID string) {
	r.mu.Lock()
	e, ok := r.streams[paneID]
	if ok {
		delete(r.streams, paneID)
		e.ended = true // suppress the exit report
	}
	r.mu.Unlock()
	if ok && e.handle != nil {
		e.handle.Close()
	}
}
