// Package eventhub is the ONE event fan-out: a publisher never blocks on
// a slow subscriber and no distinct change is ever dropped. It grew out
// of the local store's hub (2026-07) and is now shared with the remote
// transport, whose own 64-slot channel hub silently dropped events for a
// stalled Subscribe stream — the exact class the store's hub had already
// fixed once.
//
// Each subscriber owns a coalescing queue drained by a pump goroutine:
//
//   - The queue is keyed by the changed entity (the caller's key func: a
//     tile id, a grid id, a removal). A newer event for the same entity
//     REPLACES the older undelivered one in place — the semantics of the
//     client cache, which upserts by id, so skipping an intermediate
//     state is indistinguishable from having applied it.
//   - Distinct entities are never coalesced away, so the queue is bounded
//     by the number of entities touched while the consumer stalls — not
//     by an arbitrary buffer whose overflow drops events.
//   - An unkeyable event (key "") gets a unique key and is never coalesced.
package eventhub

import (
	"strconv"
	"sync"
)

// Hub fans events of type T out to every subscriber.
type Hub[T any] struct {
	key  func(T) string
	mu   sync.Mutex
	subs map[*subscriber[T]]struct{}
}

// New returns an empty hub whose subscribers coalesce by key(ev).
func New[T any](key func(T) string) *Hub[T] {
	return &Hub[T]{key: key, subs: map[*subscriber[T]]struct{}{}}
}

type subscriber[T any] struct {
	mu      sync.Mutex
	keys    []string      // delivery order: first touch of each entity
	pending map[string]T  // latest event per entity key
	seq     int           // fallback key counter for unkeyable events
	wake    chan struct{} // pump signal, capacity 1
	done    chan struct{} // closed by cancel
	out     chan T        // consumer-facing stream, closed by the pump
}

// Subscribe registers a subscriber and returns its event stream. Call
// the returned cancel func to detach; the stream is closed by the pump.
func (h *Hub[T]) Subscribe() (<-chan T, func()) {
	sub := &subscriber[T]{
		pending: map[string]T{},
		wake:    make(chan struct{}, 1),
		done:    make(chan struct{}),
		out:     make(chan T, 16),
	}
	h.mu.Lock()
	h.subs[sub] = struct{}{}
	h.mu.Unlock()
	go sub.pump()
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, sub)
			h.mu.Unlock()
			close(sub.done)
		})
	}
	return sub.out, cancel
}

// Publish hands the event to every subscriber's queue. Never blocks:
// enqueue is a map write under a short mutex, and delivery happens on
// each subscriber's own pump goroutine.
func (h *Hub[T]) Publish(ev T) {
	key := h.key(ev)
	h.mu.Lock()
	subs := make([]*subscriber[T], 0, len(h.subs))
	for sub := range h.subs {
		subs = append(subs, sub)
	}
	h.mu.Unlock()
	for _, sub := range subs {
		sub.enqueue(key, ev)
	}
}

func (sub *subscriber[T]) enqueue(key string, ev T) {
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

// pump moves queued events to the consumer in first-touch order, waiting
// when the queue is empty and exiting (closing out) when the subscriber
// is cancelled. Undelivered events at cancel are discarded — the consumer
// is gone.
func (sub *subscriber[T]) pump() {
	defer close(sub.out)
	for {
		sub.mu.Lock()
		var ev T
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
