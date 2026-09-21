package inflight

import "sync"

// Writes tallies mutations between dispatch and settle. The client's write
// dispatcher is its only caller, which is what makes "no write is in flight"
// a fact about every gesture rather than about the ones whose call site
// remembered to count.
type Writes struct {
	mu         sync.Mutex
	dispatched int
	settled    int
}

// Start counts one dispatched write. Every Start is paired with a Done,
// whether the write landed, failed or parked.
func (w *Writes) Start() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.dispatched++
}

// Done releases the write Start counted.
func (w *Writes) Done() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.settled++
}

// Len is how many dispatched writes have not settled.
func (w *Writes) Len() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.dispatched - w.settled
}

// Any reports that a write is in flight.
func (w *Writes) Any() bool { return w.Len() != 0 }
