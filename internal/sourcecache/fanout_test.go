package sourcecache

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/store"
)

// A subscriber that stalls must still learn about every grid a revalidation
// changed: the fan-out coalesces per entity, it does not discard.
// emitGridChanged is the one funnel every revalidation and eviction reaches,
// so publishing through it and reading the exported stream crosses the whole
// fan-out. Prefetch is off so nothing else publishes into the run.
func TestAStalledSubscriberLosesNoGrid(t *testing.T) {
	const grids = 200
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cc := openLayer(t, local.New(st, nil), filepath.Join(t.TempDir(), "cache.db"), Options{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stall := make(chan struct{})
	release := make(chan struct{})
	got := make(chan *pb.Event, 4*grids)
	done := make(chan error, 1)
	go func() {
		done <- cc.Subscribe(ctx, &pb.SubscribeRequest{}, func(ev *pb.Event) error {
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

	awaitSubscriber(t, cc, got)

	close(stall)
	for i := 0; i < grids; i++ {
		cc.emitGridChanged("g" + strconv.Itoa(i))
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
