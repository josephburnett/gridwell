// Package outbox is the one ordered record of writes the server has not
// acknowledged, and the one rule for what to do about them.
//
// The rule: local state may be dropped only on a server verdict. A
// dispatcher that fails on transport — the server never spoke,
// clientsync.OutcomeTransport — parks its write here as a retry thunk; every
// completed attempt for the same key (success, conflict, rejection) acks it
// away. Record is that fork, in exactly one place, so no dispatcher can
// implement half of it. The retry kick drains the outbox when the link
// returns, and so does the unload flush.
//
// # What it holds, and what it does not
//
// Entries are order and retry, never a copy of the user's value. One live
// entry per key: writes here are last-writer-wins by design (a viewport, a
// frozen face, a pane arrangement, a name), so a newer parked thunk replaces
// an older one for the same key and keeps its drain position, and a newer
// successful write clears a stale parked one — which is why Record acks on
// every completion, not only on failures.
//
// A content write parks like everything else, but its thunk re-reads the
// bytes from the cache's content entry, which is their one owner (the
// textarea is a view of that entry, not a second copy). So the outbox knows
// which tiles still owe the server a write and in what order, while the
// bytes stay where the renderer reads them.
package outbox

import (
	"sync"

	"github.com/josephburnett/gridwell/client/clientsync"
)

// Key names one unacknowledged write: the operation (the dispatcher's label
// — "SetFraming", "SetURLState", "PaneLayout", "Content", …) and the id it
// targets (a tile id, or a grid id for a root framing write). One live entry
// per key.
type Key struct {
	Op string
	ID string
}

// OpContent is the label every user-content write parks under, so a tile's
// unsaved bytes have exactly one entry however many paths tried to save them
// (the debounce sweep, an ascent flush, the retry kick).
const OpContent = "Content"

// Outbox is the set of parked writes, drained in first-parked order.
type Outbox struct {
	mu    sync.Mutex
	m     map[Key]func()
	order []Key
}

// New returns an empty outbox.
func New() *Outbox {
	return &Outbox{m: map[Key]func(){}}
}

// Record is the reconcile rule: a transport failure parks the write for the
// retry kick; any other outcome — it landed, it conflicted, it was refused —
// acknowledges the key, because the server spoke and the caller's own
// reaction (refetch, surface, drop) is what resolves it from here.
//
// retry may be nil for a write with nothing to park (a create, a drag whose
// ghost snaps back visibly): the outcome still acks any stale entry.
func (o *Outbox) Record(out clientsync.Outcome, k Key, retry func()) {
	if out == clientsync.OutcomeTransport && retry != nil {
		o.Park(k, retry)
		return
	}
	o.Ack(k)
}

// Park holds retry for k, replacing any earlier thunk for the same key (last
// writer wins — the newer closure reaches the newer value). A replaced key
// keeps its original drain position.
func (o *Outbox) Park(k Key, retry func()) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.m[k]; !ok {
		o.order = append(o.order, k)
	}
	o.m[k] = retry
}

// Ack clears k: an attempt for this key completed.
func (o *Outbox) Ack(k Key) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.m[k]; !ok {
		return
	}
	delete(o.m, k)
	o.order = compactOut(o.order, k)
}

// Drain removes every parked write and returns the retry thunks in
// first-parked order. Thunks re-park themselves (through Record) when the
// retry fails on transport again, so a drain during a still-dead link
// converges back to the same outbox rather than losing entries.
func (o *Outbox) Drain() []func() {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]func(), 0, len(o.m))
	for _, k := range o.order {
		if fn, ok := o.m[k]; ok {
			out = append(out, fn)
		}
	}
	o.m = map[Key]func(){}
	o.order = nil
	return out
}

// Len reports how many writes are parked.
func (o *Outbox) Len() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.m)
}

// Keys returns the parked keys in drain order — the observability read (the
// e2e testhook, the "unsaved work" question), never a way to mutate.
func (o *Outbox) Keys() []Key {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]Key, 0, len(o.m))
	for _, k := range o.order {
		if _, ok := o.m[k]; ok {
			out = append(out, k)
		}
	}
	return out
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
