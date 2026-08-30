package store

import (
	"github.com/josephburnett/gridwell/api/rpc"
)

// The store's event fan-out is internal/eventhub, where a writer never blocks
// on a slow consumer and no distinct change is dropped. This file owns only
// the key — which entity an rpc.Event is about — so a newer event for the same
// entity can replace an older undelivered one.

// SubscribeEvents registers a subscriber and returns its event stream. Call
// the returned cancel func to detach; the stream is closed by the pump.
func (s *Store) SubscribeEvents() (<-chan rpc.Event, func()) {
	return s.hub.Subscribe()
}

// publish hands the event to every subscriber's queue. Never blocks.
func (s *Store) publish(ev rpc.Event) {
	s.hub.Publish(ev)
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
		// Keyed apart from TileChanged, and by grid: a cross-grid move emits
		// TileRemoved for the source grid then TileChanged for the
		// destination, for the same tile id, and both must reach the
		// consumer. The removal clears the source grid's view; the change
		// lands the tile in the destination.
		if ev.TileRemoved != nil {
			return "r/" + ev.TileRemoved.GridID + "/" + ev.TileRemoved.TileID
		}
	}
	return ""
}
