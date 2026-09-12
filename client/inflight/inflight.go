// Package inflight bounds every client RPC and owns, per key, whether a fetch
// is outstanding (Set) or has failed (Latch). A claim ends when its fetch
// returns or CancelIf declares its link gone; a claim that outlived its
// request would dedupe every retry away and leave a pane loading with no
// error. Only the event Subscribe and the shell WebSocket are unbounded.
package inflight

import (
	"context"
	"sort"
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

// Set is the live claims for one kind of fetch, keyed by id.
type Set struct {
	mu sync.Mutex
	d  time.Duration
	m  map[string]*claim
}

// claim is one key's in-flight fetch. Identity is the pointer, so a late
// release is told from its successor's.
type claim struct{ cancel context.CancelFunc }

func New(d time.Duration) *Set {
	return &Set{d: d, m: map[string]*claim{}}
}

// Begin claims key for one fetch; ok is false when one already holds it. The
// fetch must use the returned context, which is what CancelIf cancels, and
// must call done when it returns.
func (s *Set) Begin(key string) (ctx context.Context, done func(), ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, held := s.m[key]; held {
		return nil, nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.d)
	c := &claim{cancel: cancel}
	s.m[key] = c
	return ctx, func() { s.release(key, c) }, true
}

// Context is a bounded context with no claim. CancelIf cannot reach it, so
// the caller must cancel it.
func (s *Set) Context() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), s.d)
}

// release drops c's claim on key if c still holds it. A cancelled fetch
// returns after a fresh one has taken the key, and freeing the fresh claim
// would drop the dogpile guard for as long as it runs.
func (s *Set) release(key string, c *claim) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m[key] == c {
		delete(s.m, key)
	}
	c.cancel()
}

// CancelIf drops and cancels every claim whose key match reports, returning
// those keys sorted. The caller chooses which keys lost a link, because one
// source going dark leaves every other source's fetches still owed an answer.
func (s *Set) CancelIf(match func(key string) bool) []string {
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
	sort.Strings(keys)
	return keys
}

// Keys lists the keys with a fetch in flight, sorted.
func (s *Set) Keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0, len(s.m))
	for k := range s.m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (s *Set) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.m)
}
