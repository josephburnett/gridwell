package store

import (
	"strconv"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
)

func gridEvent(id string) rpc.Event {
	return rpc.Event{Kind: rpc.EventGridChanged, GridChanged: &rpc.GridChanged{GridID: id}}
}

// TestPublishFansOutToAllSubscribers: every open Subscribe stream sees each
// event. This is face #4 of the primary rule — a mutation is reflected to every
// open view, so two panes on the same grid stay in step.
func TestPublishFansOutToAllSubscribers(t *testing.T) {
	s := newTestStore(t)
	chA, cancelA := s.SubscribeEvents()
	defer cancelA()
	chB, cancelB := s.SubscribeEvents()
	defer cancelB()

	s.publish(gridEvent("g1"))

	for _, c := range []<-chan rpc.Event{chA, chB} {
		got := drainEvents(t, c)
		if len(got) != 1 || got[0].GridChanged.GridID != "g1" {
			t.Errorf("subscriber got %+v, want one GridChanged(g1)", got)
		}
	}
}

// TestPublishNeverBlocksAndCoalescesSameEntity: a stalled consumer must not
// stall a writer, and repeat events for the SAME entity coalesce to the latest
// (the client cache upserts by id, so skipping an intermediate state is
// indistinguishable from applying it). If publish blocked, this test hangs —
// the failure mode the old drop-on-overflow guarded against; coalescing keeps
// the no-block property without the drops.
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
	if last := got[len(got)-1]; last.GridChanged.GridID != "g" {
		t.Errorf("last event = %+v, want GridChanged(g)", last)
	}
}

// TestPublishNeverDropsDistinctEntities: the fix for the silent-drop hole.
// With the old fixed 64-slot buffer, publishing N>64 events to a stalled
// consumer dropped the excess — a dropped TileChanged left a pane stale until
// an unrelated event touched the same grid. Distinct entities must ALL arrive.
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
		seen[ev.GridChanged.GridID] = true
	}
	if len(seen) != n {
		t.Errorf("distinct grids delivered = %d, want %d (nothing dropped)", len(seen), n)
	}
}

// TestRemovalNeverMaskedByPendingChange: removals key separately from changes
// (a cross-grid move emits both for one tile id), so however many changes are
// pending, the consumer must END at "removed" — never at a stale "changed"
// that resurrects the tile.
func TestRemovalNeverMaskedByPendingChange(t *testing.T) {
	s := newTestStore(t)
	ch, cancel := s.SubscribeEvents()
	defer cancel()

	for i := 0; i < 50; i++ {
		s.publish(rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: rpc.Tile{ID: "7", GridID: "g"}}})
	}
	s.publish(rpc.Event{Kind: rpc.EventTileRemoved, TileRemoved: &rpc.TileRemoved{GridID: "g", TileID: "7"}})

	got := drainEvents(t, ch)
	if len(got) == 0 {
		t.Fatal("no events delivered")
	}
	if last := got[len(got)-1]; last.Kind != rpc.EventTileRemoved {
		t.Errorf("last event for the tile = %v, want the removal to win", last.Kind)
	}
}

// TestCancelDetachesSubscriber: after cancel, a subscriber no longer receives
// events (it's removed from the set) and a later publish doesn't panic on the
// now-closed channel.
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
