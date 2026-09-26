package inflight

import (
	"context"
	"slices"
)

// Verdict is what one read's outcome says about its key's failure latch;
// clientsync.ReactRead is the table that produces it.
type Verdict int

const (
	// Answered clears both latches.
	Answered Verdict = iota
	// Refused is the server's verdict, the same answer every time it is
	// asked: it stands until the entity changes, its source's health
	// changes, the stream reconnects, or the user arrives somewhere new.
	Refused
	// Unreachable is the server never speaking. It stands until the same
	// signals, or the next Backstop tick, whichever is first.
	Unreachable
)

// Reads is one kind of read the renderer asks for on every draw: its claims
// and its two failure latches in one value, so a failure cannot be left to
// re-ask on the next frame. A read Settles what it heard; Ask refuses while
// the key is in flight or latched.
type Reads struct {
	claims      *claimSet
	refused     *latch
	unreachable *latch
}

func NewReads() *Reads {
	return &Reads{claims: newClaimSet(Deadline), refused: newLatch(), unreachable: newLatch()}
}

// Ask is claimSet.begin, refused too while key is latched.
func (r *Reads) Ask(key string) (ctx context.Context, done func() bool, ok bool) {
	if r.Failed(key) {
		return nil, nil, false
	}
	return r.claims.begin(key)
}

// Context is a bounded context with no claim, for a read that must not be
// deduped away; see claimSet.bounded.
func (r *Reads) Context() (context.Context, context.CancelFunc) {
	return r.claims.bounded()
}

// Settle applies one read's verdict to key.
func (r *Reads) Settle(key string, v Verdict) {
	switch v {
	case Answered:
		r.refused.clear(key)
		r.unreachable.clear(key)
	case Refused:
		r.unreachable.clear(key)
		r.refused.set(key)
	case Unreachable:
		r.refused.clear(key)
		r.unreachable.set(key)
	}
}

// Failed reports whether key is latched either way.
func (r *Reads) Failed(key string) bool {
	return r.refused.has(key) || r.unreachable.has(key)
}

// Refused reports whether the server's last word on key was a verdict.
func (r *Reads) Refused(key string) bool {
	return r.refused.has(key)
}

// FailedKeys lists every latched key, sorted.
func (r *Reads) FailedKeys() []string {
	return merged(r.refused.keys(), r.unreachable.keys())
}

// Change clears key's latches: the entity changed, so the last answer is no
// longer the answer.
func (r *Reads) Change(key string) {
	r.Settle(key, Answered)
}

// Backstop clears every Unreachable latch and returns those keys, sorted, so
// the caller re-asks each once per tick rather than once per frame.
func (r *Reads) Backstop() []string {
	keys := r.unreachable.keys()
	r.unreachable.reset()
	return keys
}

// ClearIf clears both latches for every key match reports and returns those
// keys, sorted: a source whose link came back deserves a fresh attempt.
func (r *Reads) ClearIf(match func(key string) bool) []string {
	var keys []string
	for _, k := range r.FailedKeys() {
		if match(k) {
			keys = append(keys, k)
		}
	}
	r.refused.clearIf(match)
	r.unreachable.clearIf(match)
	return keys
}

// CancelIf is claimSet.cancelIf over the claims.
func (r *Reads) CancelIf(match func(key string) bool) []string {
	return r.claims.cancelIf(match)
}

// Reset clears every latch, for arriving somewhere new. Claims stay: a read
// in flight still answers.
func (r *Reads) Reset() {
	r.refused.reset()
	r.unreachable.reset()
}

// InFlight lists the keys with a read in flight, sorted.
func (r *Reads) InFlight() []string {
	return r.claims.keys()
}

func merged(a, b []string) []string {
	out := append(slices.Clone(a), b...)
	slices.Sort(out)
	return slices.Compact(out)
}
