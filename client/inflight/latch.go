package inflight

import (
	"sort"
	"sync"
)

// Latch is the failed keys for one kind of fetch: a server verdict is the
// same answer every time it is asked, so it is held until a path that
// justifies a retry clears it. The renderer reads a latched key every frame,
// so anything that re-asks on its own turns one verdict into a per-frame loop.
type Latch struct {
	mu sync.Mutex
	m  map[string]bool
}

func NewLatch() *Latch {
	return &Latch{m: map[string]bool{}}
}

// Set latches key. Transport failures are not verdicts and must not.
func (l *Latch) Set(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.m[key] = true
}

// Has reports whether key is latched.
func (l *Latch) Has(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.m[key]
}

// Clear unlatches key: the one asker that earned a fresh attempt.
func (l *Latch) Clear(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.m, key)
}

// ClearIf unlatches every key match reports. The caller chooses which keys
// lost a link, because one source going dark says nothing about another's.
func (l *Latch) ClearIf(match func(key string) bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k := range l.m {
		if match(k) {
			delete(l.m, k)
		}
	}
}

// Reset unlatches everything, for arriving somewhere new.
func (l *Latch) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.m = map[string]bool{}
}

// Keys lists the latched keys, sorted.
func (l *Latch) Keys() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	keys := make([]string, 0, len(l.m))
	for k := range l.m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
