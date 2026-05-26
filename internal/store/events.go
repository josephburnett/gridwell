package store

import (
	"github.com/josephburnett/gridwell/internal/rpc"
)

// subscriber is one Subscribe stream connected to the store.
type subscriber struct {
	ch chan rpc.Event
}

// SubscribeEvents registers a subscriber. The returned channel is buffered
// (size 64) so a brief stall in the consumer does not stall a writer. Call
// the returned cancel func to detach.
func (s *Store) SubscribeEvents() (<-chan rpc.Event, func()) {
	sub := &subscriber{ch: make(chan rpc.Event, 64)}
	s.mu.Lock()
	s.subs[sub] = struct{}{}
	s.mu.Unlock()
	cancel := func() {
		s.mu.Lock()
		delete(s.subs, sub)
		s.mu.Unlock()
		close(sub.ch)
	}
	return sub.ch, cancel
}

// publish sends the event to every subscriber. Dropped events are silently
// ignored; clients self-heal on next read.
func (s *Store) publish(ev rpc.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sub := range s.subs {
		select {
		case sub.ch <- ev:
		default:
		}
	}
}
