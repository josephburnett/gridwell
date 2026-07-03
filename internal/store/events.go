package store

import (
	"strconv"
	"sync"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// subscriber is one Subscribe stream connected to the store. Events flow
// through a per-subscriber coalescing queue drained by a pump goroutine, so a
// writer NEVER blocks on a slow consumer and no distinct change is ever
// dropped:
//
//   - The queue is keyed by the changed entity (tile id / grid id / removal).
//     A newer event for the same entity REPLACES the older one in place —
//     exactly the semantics of the client cache, which upserts by id, so
//     skipping an intermediate state is indistinguishable from having applied
//     it. Removals key separately (see eventKey), so a change never masks a
//     removal or vice versa.
//   - Distinct entities are never coalesced away, so the queue is bounded by
//     the number of entities touched while the consumer stalls — not by an
//     arbitrary buffer whose overflow silently dropped events. (The old 64-slot
//     channel dropped on overflow; a dropped TileChanged left a pane stale
//     until some unrelated event happened to touch the same grid.)
type subscriber struct {
	mu      sync.Mutex
	keys    []string             // delivery order: first touch of each entity
	pending map[string]rpc.Event // latest event per entity key
	seq     int                  // fallback key counter for unkeyable events
	wake    chan struct{}        // pump signal, capacity 1
	done    chan struct{}        // closed by cancel
	out     chan rpc.Event       // consumer-facing stream, closed by the pump
}

// SubscribeEvents registers a subscriber and returns its event stream. Call
// the returned cancel func to detach; the stream is closed by the pump.
func (s *Store) SubscribeEvents() (<-chan rpc.Event, func()) {
	sub := &subscriber{
		pending: map[string]rpc.Event{},
		wake:    make(chan struct{}, 1),
		done:    make(chan struct{}),
		out:     make(chan rpc.Event, 16),
	}
	s.mu.Lock()
	s.subs[sub] = struct{}{}
	s.mu.Unlock()
	go sub.pump()
	cancel := func() {
		s.mu.Lock()
		delete(s.subs, sub)
		s.mu.Unlock()
		close(sub.done)
	}
	return sub.out, cancel
}

// publish hands the event to every subscriber's queue. Never blocks: enqueue
// is a map write under a short mutex, and delivery happens on each
// subscriber's own pump goroutine.
func (s *Store) publish(ev rpc.Event) {
	s.mu.Lock()
	subs := make([]*subscriber, 0, len(s.subs))
	for sub := range s.subs {
		subs = append(subs, sub)
	}
	s.mu.Unlock()
	for _, sub := range subs {
		sub.enqueue(ev)
	}
}

// eventKey identifies the entity an event is about, so a newer event for the
// same entity can replace an older undelivered one. "" means unkeyable (an
// unknown kind); enqueue gives those a unique key so they are never coalesced.
func eventKey(ev rpc.Event) string {
	switch ev.Kind {
	case rpc.EventGridChanged:
		if ev.GridChanged != nil {
			return "g/" + ev.GridChanged.GridID
		}
	case rpc.EventTileChanged:
		if ev.TileChanged != nil {
			return "t/" + ev.TileChanged.Tile.ID
		}
	case rpc.EventTileRemoved:
		// Keyed apart from TileChanged (and by grid): a cross-grid MoveTile
		// emits TileRemoved(source grid) then TileChanged(dest grid) for the
		// SAME tile id, and both must reach the consumer — the removal clears
		// the source grid's view, the change lands the tile in the dest.
		if ev.TileRemoved != nil {
			return "r/" + ev.TileRemoved.GridID + "/" + ev.TileRemoved.TileID
		}
	}
	return ""
}

func (sub *subscriber) enqueue(ev rpc.Event) {
	key := eventKey(ev)
	sub.mu.Lock()
	if key == "" {
		sub.seq++
		key = "u/" + strconv.Itoa(sub.seq)
	}
	if _, exists := sub.pending[key]; !exists {
		sub.keys = append(sub.keys, key)
	}
	sub.pending[key] = ev
	sub.mu.Unlock()
	select {
	case sub.wake <- struct{}{}:
	default:
	}
}

// pump moves queued events to the consumer in first-touch order, waiting when
// the queue is empty and exiting (closing out) when the subscriber is
// cancelled. Undelivered events at cancel are discarded — the consumer is gone.
func (sub *subscriber) pump() {
	defer close(sub.out)
	for {
		sub.mu.Lock()
		var ev rpc.Event
		have := len(sub.keys) > 0
		if have {
			k := sub.keys[0]
			sub.keys = sub.keys[1:]
			ev = sub.pending[k]
			delete(sub.pending, k)
		}
		sub.mu.Unlock()
		if !have {
			select {
			case <-sub.wake:
				continue
			case <-sub.done:
				return
			}
		}
		select {
		case sub.out <- ev:
		case <-sub.done:
			return
		}
	}
}
