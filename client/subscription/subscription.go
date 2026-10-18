// Package subscription decides when this client tells the node what it shows
// (pane.View is what): once per change to any of it, and again after every
// event stream re-open, because the node forgets a session whose stream
// closed.
package subscription

import (
	"sync"

	"google.golang.org/protobuf/proto"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// Tracker is the one sender's state. A view that failed to send is not retried
// until it changes or the stream re-opens, so a refusal cannot become a call
// per frame.
type Tracker struct {
	mu   sync.Mutex
	want *gridwellv1.SetViewRequest
	last *gridwellv1.SetViewRequest
	sent bool // last was sent since the stream last re-opened
}

// Show records the view on screen, true when it is owed to the node.
func (t *Tracker) Show(v *gridwellv1.SetViewRequest) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.want = proto.CloneOf(v)
	return t.owed()
}

// Next claims the view to send now, false when the node already has it.
func (t *Tracker) Next() (*gridwellv1.SetViewRequest, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.owed() {
		return nil, false
	}
	t.last, t.sent = t.want, true
	return proto.CloneOf(t.want), true
}

// Reopened says the event stream is being opened again.
func (t *Tracker) Reopened() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sent = false
}

func (t *Tracker) owed() bool { return !t.sent || !proto.Equal(t.last, t.want) }
