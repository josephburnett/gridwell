package inflight

import "sync"

// Batch holds the reads asked during a paint, so they leave as one call
// when the paint ends; a read asked outside a paint leaves at once. The
// frame is the batch: there is no timer.
type Batch[T any] struct {
	mu      sync.Mutex
	depth   int
	pending []T
}

// Begin opens a paint; paints nest.
func (b *Batch[T]) Begin() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.depth++
}

// End closes a paint and returns what to read now: everything asked since
// the outermost Begin, once that one ends.
func (b *Batch[T]) End() []T {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.depth--
	if b.depth > 0 {
		return nil
	}
	out := b.pending
	b.pending = nil
	return out
}

// Add asks for t and returns what to read now: t alone outside a paint,
// nothing inside one.
func (b *Batch[T]) Add(t T) []T {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.depth == 0 {
		return []T{t}
	}
	b.pending = append(b.pending, t)
	return nil
}
