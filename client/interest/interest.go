// Package interest decides when this client tells the node what it shows
// (pane.Showing is what): once per change, and again after every event stream
// re-open, because the node forgets a session whose stream closed.
package interest

import (
	"slices"
	"sync"
)

// Tracker is the one sender's state. A set that failed to send is not retried
// until it changes or the stream re-opens, so a refusal cannot become a call
// per frame.
type Tracker struct {
	mu   sync.Mutex
	want []string
	last []string
	sent bool // last was sent since the stream last re-opened
}

// Show records the set on screen, true when it is owed to the node.
func (t *Tracker) Show(set []string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.want = slices.Clone(set)
	return t.owed()
}

// Next claims the set to send now, false when the node already has it.
func (t *Tracker) Next() ([]string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.owed() {
		return nil, false
	}
	t.last, t.sent = slices.Clone(t.want), true
	return slices.Clone(t.want), true
}

// Reopened says the event stream is being opened again.
func (t *Tracker) Reopened() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sent = false
}

func (t *Tracker) owed() bool { return !t.sent || !slices.Equal(t.last, t.want) }
