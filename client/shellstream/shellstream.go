// Package shellstream owns the lifecycle of the client's live shell
// attachments, keyed by the content tile whose session each attaches: one live
// surface per content tile, so a pane taking a terminal over moves it without
// the registry knowing. Open replaces the tile's existing stream; Write and
// Resize outside a stream are silent no-ops, a teardown racing an in-flight
// keystroke being expected. An end fires at most once and only while that
// stream is still the tile's current one, and output routes through the
// registry rather than the closure, so a replaced stream's late end or late
// bytes cannot reach the renderer.
package shellstream

import "sync"

// Handle is the write side of one live attachment, as a Dialer returns it.
type Handle interface {
	Write(data []byte)
	Resize(cols, rows int)
	// Close ends the attachment from this side. The dialer must still
	// deliver onEnd exactly once.
	Close()
}

// Dialer opens one attachment bound to tileID. onEnd fires exactly once, with
// message "" for a clean end. sessionGone is the server's verdict that the
// session is gone, which the caller reads to hide the refresh affordance.
type Dialer func(tileID string, cols, rows int, onData func(data []byte), onEnd func(message string, sessionGone bool)) Handle

// Exit is an unexpected end reported to the caller.
type Exit struct {
	TileID      string
	Message     string
	SessionGone bool
}

type entry struct {
	handle Handle
	ended  bool
}

type Registry struct {
	dial   Dialer
	onData func(tileID string, data []byte)
	onExit func(Exit)

	mu      sync.Mutex
	streams map[string]*entry
}

// New wires a registry to its dialer and the two renderer callbacks. Both run
// outside the registry's lock, so a callback may call back in.
func New(dial Dialer, onData func(tileID string, data []byte), onExit func(Exit)) *Registry {
	return &Registry{dial: dial, onData: onData, onExit: onExit, streams: map[string]*entry{}}
}

// Open replaces whatever that tile held.
func (r *Registry) Open(tileID string, cols, rows int) {
	r.Close(tileID)
	// The slot is claimed before the dial: a dialer that fails instantly calls
	// onEnd synchronously, and an unclaimed slot would swallow that report.
	e := &entry{}
	r.mu.Lock()
	r.streams[tileID] = e
	r.mu.Unlock()
	handle := r.dial(
		tileID, cols, rows,
		func(data []byte) {
			if !r.current(tileID, e) {
				return
			}
			r.onData(tileID, data)
		},
		func(message string, sessionGone bool) {
			r.mu.Lock()
			if e.ended { // exactly-once, whatever raced
				r.mu.Unlock()
				return
			}
			e.ended = true
			if r.streams[tileID] != e { // replaced/closed: the caller already knows
				r.mu.Unlock()
				return
			}
			delete(r.streams, tileID)
			r.mu.Unlock()
			r.onExit(Exit{TileID: tileID, Message: message, SessionGone: sessionGone})
		},
	)
	r.mu.Lock()
	e.handle = handle
	ended := e.ended
	r.mu.Unlock()
	if ended {
		// The dial failed instantly, so onEnd already reported it and nothing
		// else holds this handle.
		handle.Close()
	}
}

func (r *Registry) current(tileID string, e *entry) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.streams[tileID] == e
}

func (r *Registry) Write(tileID string, data []byte) {
	if h := r.handle(tileID); h != nil {
		h.Write(data)
	}
}

func (r *Registry) Resize(tileID string, cols, rows int) {
	if h := r.handle(tileID); h != nil {
		h.Resize(cols, rows)
	}
}

func (r *Registry) handle(tileID string) Handle {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.streams[tileID]; ok {
		return e.handle
	}
	return nil
}

// Close suppresses the exit report, because this side asked.
func (r *Registry) Close(tileID string) {
	r.mu.Lock()
	e, ok := r.streams[tileID]
	if ok {
		delete(r.streams, tileID)
		e.ended = true
	}
	r.mu.Unlock()
	if ok && e.handle != nil {
		e.handle.Close()
	}
}
