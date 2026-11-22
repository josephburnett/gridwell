package inflight

import (
	"context"
	"slices"
	"sync"
)

// Verdict is what one read's outcome says about its key's failure latch;
// clientsync.ReactRead is the table that produces it.
type Verdict int

const (
	// Answered clears the latch.
	Answered Verdict = iota
	// Refused is the server's verdict, the same answer every time it is
	// asked: it stands until the entity changes, its source's health
	// changes, the stream reconnects, or the user arrives somewhere new.
	Refused
	// Unreachable is the server never speaking. It stands until the same
	// signals, or the next Backstop tick, whichever is first.
	Unreachable
	// Dead is a Refused whose verdict is that the key's path ends in nothing
	// (gwerr.IsDeadLink). It stands and clears as Refused does, and ReviveIf
	// clears it alone; Reads.Dead is what tells it apart.
	Dead
	// Abandoned is a read the client cancelled itself (CancelIf). Nothing
	// was heard, so it moves no latch; the canceller owns the re-ask.
	Abandoned
)

// Reads is one kind of read the renderer asks for on every draw: its claims
// and its failure latch in one value, so a failure cannot be left to re-ask
// on the next draw. A read Settles what it heard; Ask refuses while the key
// is in flight or latched.
type Reads struct {
	claims *claimSet
	mu     sync.Mutex
	// latched is each failed key's verdict, Refused, Dead or Unreachable,
	// held until a path that justifies a retry clears it.
	latched map[string]Verdict
}

func NewReads() *Reads {
	return &Reads{claims: newClaimSet(Deadline), latched: map[string]Verdict{}}
}

// Ask claims key for one read (see claimSet.join), refused too while key is
// latched.
func (r *Reads) Ask(key string) (ctx context.Context, done func() bool, ok bool) {
	ctx, done, _, ok = r.Join(key)
	return ctx, done, ok
}

// Join is Ask for a caller that must have the answer: end closes when the
// read that answers key returns, whether Join claimed it or one was already
// in flight. A latched key is neither read nor waited on, and end is nil.
func (r *Reads) Join(key string) (ctx context.Context, done func() bool, end <-chan struct{}, ok bool) {
	if r.Failed(key) {
		return nil, nil, nil, false
	}
	return r.claims.join(key)
}

// Context is a bounded context with no claim, for a read that must not be
// deduped away; see claimSet.bounded.
func (r *Reads) Context() (context.Context, context.CancelFunc) {
	return r.claims.bounded()
}

// Settle applies one read's verdict to key.
func (r *Reads) Settle(key string, v Verdict) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch v {
	case Answered:
		delete(r.latched, key)
	case Refused, Dead, Unreachable:
		r.latched[key] = v
	}
}

func (r *Reads) verdict(key string) Verdict {
	r.mu.Lock()
	defer r.mu.Unlock()
	if v, ok := r.latched[key]; ok {
		return v
	}
	return Answered
}

// Dead reports whether the server's last word on key was the dead verdict.
func (r *Reads) Dead(key string) bool { return r.verdict(key) == Dead }

// Failed reports whether key is latched, by any verdict.
func (r *Reads) Failed(key string) bool { return r.verdict(key) != Answered }

// Refused reports whether the server's last word on key was a verdict.
func (r *Reads) Refused(key string) bool {
	v := r.verdict(key)
	return v == Refused || v == Dead
}

// FailedKeys lists every latched key, sorted.
func (r *Reads) FailedKeys() []string {
	return r.latchedKeys(func(string, Verdict) bool { return true }, false)
}

// latchedKeys lists the latched keys match reports, sorted, and unlatches
// them when clear says so.
func (r *Reads) latchedKeys(match func(key string, v Verdict) bool, clear bool) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var keys []string
	for k, v := range r.latched {
		if match(k, v) {
			keys = append(keys, k)
			if clear {
				delete(r.latched, k)
			}
		}
	}
	slices.Sort(keys)
	return keys
}

// Change clears key's latch and owes a read in flight a re-ask: the entity
// changed, so neither the last answer nor the one on the wire is the answer.
// It is the only thing that owes one.
func (r *Reads) Change(key string) {
	r.claims.owe(key)
	r.Settle(key, Answered)
}

// Backstop clears every Unreachable latch and returns those keys, sorted, so
// the caller re-asks each once per tick rather than once per draw.
func (r *Reads) Backstop() []string {
	return r.latchedKeys(func(_ string, v Verdict) bool { return v == Unreachable }, true)
}

// ClearIf clears the latch for every key match reports and returns those
// keys, sorted: a source whose link came back deserves a fresh attempt.
// The caller chooses which keys lost a link, because one source going dark
// says nothing about another's.
func (r *Reads) ClearIf(match func(key string) bool) []string {
	return r.latchedKeys(func(k string, _ Verdict) bool { return match(k) }, true)
}

// ReviveIf clears the dead verdict on every key match reports and returns
// those keys, sorted: the namespace that gave it changed, and a gone key may
// be back. Every other verdict stands.
func (r *Reads) ReviveIf(match func(key string) bool) []string {
	return r.latchedKeys(func(k string, v Verdict) bool { return v == Dead && match(k) }, true)
}

// Strand is claimSet.strandIf over the claims.
func (r *Reads) Strand(match func(key string) bool) { r.claims.strandIf(match) }

// CancelIf is claimSet.cancelIf over the claims.
func (r *Reads) CancelIf(match func(key string) bool) []string {
	return r.claims.cancelIf(match)
}

// Reset clears every latch, for arriving somewhere new. Claims stay: a read
// in flight still answers.
func (r *Reads) Reset() {
	r.latchedKeys(func(string, Verdict) bool { return true }, true)
}

// InFlight lists the keys with a read in flight, sorted.
func (r *Reads) InFlight() []string {
	return r.claims.keys()
}
