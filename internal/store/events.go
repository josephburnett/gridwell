package store

import (
	"github.com/josephburnett/gridwell/internal/rpc"
)

// subscriber is one Subscribe stream connected to the store. The store
// publishes mutation events to every subscriber for the affected user.
//
// Events are dispatched to the subscriber's channel; if the buffer is full
// (the consumer is slow), the event is dropped. This is safe because clients
// reconcile from the canonical state on the next read; missing an event means
// at most a brief lag, not corruption.
type subscriber struct {
	userID int64
	ch     chan rpc.Event
}

// SubscribeEvents registers a subscriber for events affecting userID. The
// returned channel is buffered (size 64) so a brief stall in the consumer
// does not stall a writer. Call the returned cancel func to detach.
func (s *Store) SubscribeEvents(userID int64) (<-chan rpc.Event, func()) {
	sub := &subscriber{userID: userID, ch: make(chan rpc.Event, 64)}
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

// publish sends the event to every subscriber for userID. Dropped events are
// silently ignored; clients self-heal on next read.
func (s *Store) publish(userID int64, ev rpc.Event) {
	// Populate the runtime-only `Live` field on any TileChanged payload
	// before sending. Done here so every emitter — tiles.go, cow.go,
	// move_clone.go, url.go — gets the field for free.
	if ev.TileChanged != nil && ev.TileChanged.Tile.IsURL() && s.urlDriver != nil {
		ev.TileChanged.Tile.Live = s.urlDriver.IsLive(userID, ev.TileChanged.Tile.ID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for sub := range s.subs {
		if sub.userID != userID {
			continue
		}
		select {
		case sub.ch <- ev:
		default:
		}
	}
}
