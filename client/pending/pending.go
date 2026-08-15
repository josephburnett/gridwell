// Package pending is the ONE owner of durable writes the server has not
// acknowledged (2026-08-14, the transport-loss class fix). Before it,
// every framing/freeze write was fire-and-forget: a transport failure
// surfaced a notice and abandoned the payload — the settled viewport, the
// wheel-zoomed well view, a url tile's whole browsing trail, a workspace
// layout — each loss shaped slightly differently at its own call site.
//
// Now a dispatcher that fails on TRANSPORT (the server never spoke —
// clientsync.OutcomeTransport) parks the write here as a retry thunk, and
// every completed attempt for the same key (success or server verdict)
// acknowledges it away. The retry kick drains the ledger when the link
// returns. Framing is last-writer-wins by design, so one entry per key —
// a newer parked value replaces an older one, and a newer SUCCESSFUL
// write clears a stale parked one (Ack on every completion, not just
// failures, is what makes that hold).
//
// The thunks re-enter their own dispatcher, so a retry that fails on
// transport parks itself again, and a retry that hits a version conflict
// gets the dispatcher's normal one-shot re-claim. Content bytes are NOT
// held here — the cache's dirty entries are their ledger; this package
// covers everything else durable.
package pending

import "sync"

// Key names one unacknowledged write: the operation (the dispatcher's
// label — "SetWellView", "SetURLState", "PaneLayout", …) and the id it
// targets. One live entry per key.
type Key struct {
	Op string
	ID string
}

// Ledger is the set of parked writes, drained in first-parked order.
type Ledger struct {
	mu    sync.Mutex
	m     map[Key]func()
	order []Key
}

// New returns an empty ledger.
func New() *Ledger {
	return &Ledger{m: map[Key]func(){}}
}

// Put parks retry for k, replacing any earlier thunk for the same key
// (last writer wins — the newer closure carries the newer value). A
// replaced key keeps its original drain position.
func (l *Ledger) Put(k Key, retry func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.m[k]; !ok {
		l.order = append(l.order, k)
	}
	l.m[k] = retry
}

// Ack clears k: an attempt for this key COMPLETED — landed or was
// refused by the server. Called on every completion, so a fresh write
// that succeeds clears a stale parked one instead of letting the drain
// replay old state over it.
func (l *Ledger) Ack(k Key) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.m[k]; !ok {
		return
	}
	delete(l.m, k)
	l.order = compactOut(l.order, k)
}

// Drain removes every parked write and returns the retry thunks in
// first-parked order. Thunks re-park themselves (via their dispatcher) if
// the retry fails on transport again, so a drain during a still-dead link
// converges back to the same ledger rather than losing entries.
func (l *Ledger) Drain() []func() {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]func(), 0, len(l.m))
	for _, k := range l.order {
		if fn, ok := l.m[k]; ok {
			out = append(out, fn)
		}
	}
	l.m = map[Key]func(){}
	l.order = nil
	return out
}

// Len reports how many writes are parked.
func (l *Ledger) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.m)
}

func compactOut(order []Key, k Key) []Key {
	out := order[:0]
	for _, o := range order {
		if o != k {
			out = append(out, o)
		}
	}
	return out
}
