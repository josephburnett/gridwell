package pluginhost

import (
	"context"
	"strconv"
	"testing"
	"time"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// A subscriber that stalls must still learn about every grid that changed
// while it was stalled: the fan-out coalesces per entity, it does not
// discard. emitGridChanged is the one funnel every write of this adapter
// reaches (see changed), so publishing through it and reading the exported
// stream crosses the whole fan-out.
func TestAStalledSubscriberLosesNoGrid(t *testing.T) {
	const grids = 200
	a := New(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stall := make(chan struct{})
	release := make(chan struct{})
	got := make(chan *gridwellv1.Event, 4*grids)
	done := make(chan error, 1)
	go func() {
		done <- a.Subscribe(ctx, &gridwellv1.SubscribeRequest{}, func(ev *gridwellv1.Event) error {
			select {
			case <-stall:
				<-release
			default:
			}
			select {
			case got <- ev:
			case <-ctx.Done():
			}
			return nil
		})
	}()

	awaitSubscriber(t, a, got)

	close(stall)
	for i := 0; i < grids; i++ {
		a.emitGridChanged("g" + strconv.Itoa(i))
	}
	close(release)

	want := map[string]bool{}
	for i := 0; i < grids; i++ {
		want["g"+strconv.Itoa(i)] = true
	}
	giveUp := time.After(20 * time.Second)
	for len(want) > 0 {
		select {
		case ev := <-got:
			delete(want, ev.GetGridChanged().GetGridId())
		case <-giveUp:
			t.Fatalf("%d of %d changed grids never reached the stalled subscriber", len(want), grids)
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Subscribe ended %v, want a clean end on ctx cancel", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Subscribe did not end when its ctx was canceled")
	}
}

// awaitSubscriber waits for a Subscribe riding a goroutine to attach, and
// leaves the stream as it found it. Attaching is synchronous, but the call
// that does it is a goroutine, so an event published before it lands is
// nobody's: publish until one comes back, then drain what the probing left
// behind with one last sentinel, which delivery in first-touch order puts
// after every probe.
func awaitSubscriber(t *testing.T, a *Adapter, seen <-chan *gridwellv1.Event) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for attached := false; !attached; {
		a.emitGridChanged("attach-probe")
		select {
		case <-seen:
			attached = true
		case <-time.After(20 * time.Millisecond):
			if time.Now().After(deadline) {
				t.Fatal("the stream never registered the subscriber")
			}
		}
	}
	a.emitGridChanged("attach-done")
	for {
		select {
		case ev := <-seen:
			if ev.GetGridChanged().GetGridId() == "attach-done" {
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the attach sentinel never arrived; the stream is stuck")
		}
	}
}
