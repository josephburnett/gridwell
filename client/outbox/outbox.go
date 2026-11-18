// Package outbox is the ordered record of writes the server has not
// acknowledged. A write parks as a retry thunk when it is sent, not on its
// answer, because a request the network eats never produces one. An entry is
// order and retry, never a copy of the user's value: every parked write is a
// last-writer-wins overwrite of one key, and a content thunk re-reads the bytes
// from the cache entry that owns them.
package outbox

import (
	"sync"

	"github.com/josephburnett/gridwell/client/clientsync"
)

// Key names one unacknowledged write: the dispatcher's label, such as
// "SetFraming", and the tile or grid id it targets.
type Key struct {
	Op string
	ID string
}

// OpContent is the label every user-content write parks under, so a tile's
// unsaved bytes have one entry however many paths tried to save them.
const OpContent = "Content"

// Outbox drains in first-parked order.
type Outbox struct {
	mu    sync.Mutex
	m     map[Key]func()
	order []Key
	// newest is the sequence of each key's newest call still out.
	newest map[Key]uint64
	seq    uint64
}

func New() *Outbox {
	return &Outbox{m: map[Key]func(){}, newest: map[Key]uint64{}}
}

// Send parks the retry thunk, runs the call, then records what the server
// said. The key stays parked while the call is out, so a drain racing the
// flight re-sends it, which is safe because every parked write overwrites one
// key. Only the newest send of a key records: an unheard call parks its
// retry and any other outcome acks the key, while an older call's answer,
// which can come back after a newer one went out, says nothing about the
// value the key now owes. retry may be nil for a write with nothing to park.
func (o *Outbox) Send(k Key, retry func(), call func() clientsync.Outcome) clientsync.Outcome {
	o.mu.Lock()
	o.seq++
	seq := o.seq
	o.newest[k] = seq
	if retry != nil {
		o.park(k, retry)
	}
	o.mu.Unlock()
	out := call()
	o.mu.Lock()
	defer o.mu.Unlock()
	switch {
	case o.newest[k] != seq:
	case clientsync.Unheard(out) && retry != nil:
		delete(o.newest, k)
		o.park(k, retry)
	default:
		delete(o.newest, k)
		o.ack(k)
	}
	return out
}

// RecordContent is Send's fork for the one op whose completion is a state,
// the dirtiness the cache's content entry owns, rather than an RPC outcome.
func (o *Outbox) RecordContent(tileID string, dirty bool, retry func()) {
	o.mu.Lock()
	defer o.mu.Unlock()
	k := Key{Op: OpContent, ID: tileID}
	if dirty {
		o.park(k, retry)
		return
	}
	o.ack(k)
}

// SyncContent re-derives the content entries from the cache's dirty set
// before a drain, covering drift that would otherwise cost the words typed
// last at a quit. It parks what is dirty and acks nothing: a key it cannot
// see is a key it must not judge.
func (o *Outbox) SyncContent(dirty []string, retry func(tileID string) func()) {
	for _, id := range dirty {
		o.RecordContent(id, true, retry(id))
	}
}

// park replaces an earlier thunk for k, the newer closure reaching the newer
// value, and keeps the key's drain position. It is unexported because that
// holds only in send order, which Send and the content entry's dirtiness
// keep. The caller holds mu, as for ack.
func (o *Outbox) park(k Key, retry func()) {
	if _, ok := o.m[k]; !ok {
		o.order = append(o.order, k)
	}
	o.m[k] = retry
}

func (o *Outbox) ack(k Key) {
	if _, ok := o.m[k]; !ok {
		return
	}
	delete(o.m, k)
	o.order = compactOut(o.order, k)
}

// Drain returns the thunks in first-parked order. A thunk re-parks itself
// through Send when its retry fails again, so a drain during a dead link
// loses no entries.
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

// Has reports whether k is still owed, so a caller that wants to say a write
// parked reads it from here rather than re-deriving Send's rule.
func (o *Outbox) Has(k Key) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, ok := o.m[k]
	return ok
}

func (o *Outbox) Len() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.m)
}

// Keys is in drain order, for reading only.
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
