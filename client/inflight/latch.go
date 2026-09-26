package inflight

import (
	"maps"
	"slices"
	"sync"
)

// latch is a set of failed keys, held until a path that justifies a retry
// clears it. Reads holds one per kind of failure and owns which path clears
// which.
type latch struct {
	mu sync.Mutex
	m  map[string]bool
}

func newLatch() *latch {
	return &latch{m: map[string]bool{}}
}

func (l *latch) set(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.m[key] = true
}

func (l *latch) has(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.m[key]
}

func (l *latch) clear(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.m, key)
}

// ClearIf unlatches every key match reports. The caller chooses which keys
// lost a link, because one source going dark says nothing about another's.
func (l *latch) clearIf(match func(key string) bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k := range l.m {
		if match(k) {
			delete(l.m, k)
		}
	}
}

func (l *latch) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.m = map[string]bool{}
}

func (l *latch) keys() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Sorted(maps.Keys(l.m))
}
