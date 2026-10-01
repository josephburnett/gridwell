// Package inflight bounds every client RPC and owns, per key, whether a read
// is outstanding or has failed. Reads is the only way to hold a claim: the
// claim set and the failure latches are unexported, so no caller can take a
// claim without the latch that stops a failure being asked every frame. A
// claim ends when its fetch returns or CancelIf declares its link gone. Only
// the event Subscribe and the shell WebSocket are unbounded.
package inflight

import (
	"context"
	"maps"
	"slices"
	"sync"
	"time"
)

// Deadline is the outside bound on any one client RPC: long enough for a
// plugin's first listing over a slow link, short enough that a request lost
// to a dead socket becomes a visible failure.
const Deadline = 30 * time.Second

// Bounded is the context for a client RPC that holds no dedupe claim: one
// door to Deadline, so no call site picks its own bound. The caller cancels.
func Bounded() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), Deadline)
}

// claimSet is the live claims for one kind of fetch, keyed by id.
type claimSet struct {
	mu sync.Mutex
	d  time.Duration
	m  map[string]*claim
}

// claim is one key's in-flight fetch. Identity is the pointer, so a late
// release is told from its successor's.
type claim struct {
	cancel context.CancelFunc
	// owed records that the key changed while this claim was held: the
	// answer in flight was taken before the change, so it is not the answer.
	// A refused ask owes nothing, because a draw asks every frame and its ask
	// carries no news the read in flight does not already answer.
	owed bool
	// end closes when the fetch returns.
	end chan struct{}
}

func newClaimSet(d time.Duration) *claimSet {
	return &claimSet{d: d, m: map[string]*claim{}}
}

// join claims key for one fetch; ok is false when one already holds it. The
// fetch must use the returned context, which is what CancelIf cancels, and
// must call done when it returns; done reports whether the key was owed a
// re-ask while the claim was held. end closes when the fetch holding key
// returns, whether this call claimed it or another one already had.
func (s *claimSet) join(key string) (ctx context.Context, done func() bool, end <-chan struct{}, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, held := s.m[key]; held {
		return nil, nil, c.end, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.d)
	c := &claim{cancel: cancel, end: make(chan struct{})}
	s.m[key] = c
	return ctx, func() bool { return s.release(key, c) }, c.end, true
}

// owe marks key's claim owed a re-ask, if one is held. With none held the
// next read already answers after the change.
func (s *claimSet) owe(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, held := s.m[key]; held {
		c.owed = true
	}
}

// bounded is a bounded context with no claim. CancelIf cannot reach it, so
// the caller must cancel it.
func (s *claimSet) bounded() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), s.d)
}

// release drops c's claim on key if c still holds it and reports whether it
// was owed a re-ask. A cancelled fetch returns after a fresh one has
// taken the key, and freeing the fresh claim would drop the dogpile guard for
// as long as it runs; the successor carries its own owed flag.
func (s *claimSet) release(key string, c *claim) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m[key] == c {
		delete(s.m, key)
	}
	c.cancel()
	select {
	case <-c.end:
	default:
		close(c.end)
	}
	return c.owed
}

// cancelIf drops and cancels every claim whose key match reports, returning
// those keys sorted. The caller chooses which keys lost a link, because one
// source going dark leaves every other source's fetches still owed an answer.
func (s *claimSet) cancelIf(match func(key string) bool) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0, len(s.m))
	for k, c := range s.m {
		if !match(k) {
			continue
		}
		keys = append(keys, k)
		c.cancel()
		delete(s.m, k)
	}
	slices.Sort(keys)
	return keys
}

// keys lists the keys with a fetch in flight, sorted.
func (s *claimSet) keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Sorted(maps.Keys(s.m))
}
