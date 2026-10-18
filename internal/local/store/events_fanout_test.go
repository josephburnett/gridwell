package store

import (
	"strconv"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

func gridEvent(id string) *gridwellv1.Event {
	return &gridwellv1.Event{Payload: &gridwellv1.Event_GridChanged{GridChanged: &gridwellv1.GridChanged{GridId: id}}}
}

// TestPublishFansOutToAllSubscribers: every open Subscribe stream sees each
// event. This is part #4 of the primary rule — a mutation is reflected to every
// open view, so two panes on the same grid stay in step.
func TestPublishFansOutToAllSubscribers(t *testing.T) {
	s := newTestStore(t)
	chA, cancelA := s.SubscribeEvents()
	defer cancelA()
	chB, cancelB := s.SubscribeEvents()
	defer cancelB()

	s.publish(gridEvent("g1"))

	for _, c := range []<-chan *gridwellv1.Event{chA, chB} {
		got := drainEvents(t, c)
		if len(got) != 1 || got[0].GetGridChanged().GridId != "g1" {
			t.Errorf("subscriber got %+v, want one GridChanged(g1)", got)
		}
	}
}

// A stalled consumer must not stall a writer, and repeat events for the same
// entity coalesce to the latest, which the client cache cannot tell from
// applying every one. If publish blocked, this test hangs.
func TestPublishNeverBlocksAndCoalescesSameEntity(t *testing.T) {
	s := newTestStore(t)
	ch, cancel := s.SubscribeEvents()
	defer cancel()

	// Don't drain until all publishes land. Same grid every time → the
	// undelivered tail coalesces; the count stays far below the publish count
	// and the LAST event must still be delivered.
	const overflow = 500
	for i := 0; i < overflow; i++ {
		s.publish(gridEvent("g"))
	}

	got := drainEvents(t, ch)
	if len(got) == 0 || len(got) >= overflow {
		t.Fatalf("delivered %d events, want >0 and far fewer than %d (coalesced)", len(got), overflow)
	}
	if last := got[len(got)-1]; last.GetGridChanged().GridId != "g" {
		t.Errorf("last event = %+v, want GridChanged(g)", last)
	}
}

// Distinct entities must all arrive: a dropped TileChanged leaves a pane stale
// until an unrelated event touches the same grid.
func TestPublishNeverDropsDistinctEntities(t *testing.T) {
	s := newTestStore(t)
	ch, cancel := s.SubscribeEvents()
	defer cancel()

	const n = 300 // well past the old 64-slot buffer
	for i := 0; i < n; i++ {
		s.publish(gridEvent("g" + strconv.Itoa(i)))
	}

	got := drainEvents(t, ch)
	seen := map[string]bool{}
	for _, ev := range got {
		seen[ev.GetGridChanged().GridId] = true
	}
	if len(seen) != n {
		t.Errorf("distinct grids delivered = %d, want %d (nothing dropped)", len(seen), n)
	}
}

// Removals key separately from changes, a cross-grid move emitting both for
// one tile id, so however many changes are pending the consumer ends at
// removed, never at a stale change that resurrects the tile.
func TestRemovalNeverMaskedByPendingChange(t *testing.T) {
	s := newTestStore(t)
	ch, cancel := s.SubscribeEvents()
	defer cancel()

	for i := 0; i < 50; i++ {
		s.publish(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{TileChanged: &gridwellv1.TileChanged{Tile: &gridwellv1.Tile{Id: "7", GridId: "g"}}}})
	}
	s.publish(&gridwellv1.Event{Payload: &gridwellv1.Event_TileRemoved{TileRemoved: &gridwellv1.TileRemoved{GridId: "g", TileId: "7"}}})

	got := drainEvents(t, ch)
	if len(got) == 0 {
		t.Fatal("no events delivered")
	}
	if last := got[len(got)-1]; last.GetTileRemoved() == nil {
		t.Errorf("last event for the tile = %v, want the removal to win", last)
	}
}

// TestCancelDetachesSubscriber: after cancel, a subscriber is removed from
// the set and receives no more events, and a later publish does not panic on
// the closed channel.
func TestCancelDetachesSubscriber(t *testing.T) {
	s := newTestStore(t)
	ch, cancel := s.SubscribeEvents()
	cancel()

	// The channel is closed by cancel; draining yields nothing.
	if got := drainEvents(t, ch); len(got) != 0 {
		t.Errorf("cancelled subscriber drained %d events, want 0", len(got))
	}
	// Publishing after cancel must not touch the detached subscriber (no send
	// on a closed channel → no panic).
	s.publish(gridEvent("g2"))
}
