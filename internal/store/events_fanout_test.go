package store

import (
	"testing"

	"github.com/josephburnett/gridwell/internal/rpc"
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

// TestPublishNeverBlocksWhenBufferFull: a stalled consumer must not stall a
// writer. The channel is buffered (64); once full, further publishes are
// dropped (select default) rather than blocking the mutation. We publish well
// past the buffer and assert publish returns and exactly the buffer's worth is
// retained.
func TestPublishNeverBlocksWhenBufferFull(t *testing.T) {
	s := newTestStore(t)
	ch, cancel := s.SubscribeEvents()
	defer cancel()

	// Never drain ch: fill and overflow it. If publish blocked, this hangs and
	// the test times out — the failure mode we're guarding against.
	const overflow = 200
	for i := 0; i < overflow; i++ {
		s.publish(gridEvent("g"))
	}

	got := drainEvents(t, ch)
	if len(got) != 64 {
		t.Errorf("buffered events = %d, want 64 (the rest dropped, writer never blocked)", len(got))
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
