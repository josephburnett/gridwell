package files

import (
	"context"
	"sync"
	"testing"
	"time"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
)

// A grid id the OS cannot read as a path, under a root, is dead, and asking
// for it darkens nothing.
func TestAnIdNamingNoPathIsDeadAndDarkensNothing(t *testing.T) {
	f := newFixture(t)
	for _, path := range []string{f.root + "/a\x00b", "rel", ""} {
		_, err := f.s.GetGrid(context.Background(), &pb.GetGridRequest{GridId: idOf(path)})
		if !gwerr.IsDeadLink(err) {
			t.Errorf("GetGrid(%q) = %v, want dead", path, err)
		}
	}
	if h := f.s.health(); h != (health{}) {
		t.Fatalf("health after asking for ids naming no path = %+v, want every directory read", h)
	}
}

// Health transitions reach a subscriber in the order they happened, so the
// last one it hears is the files' health now, however many race.
func TestHealthIsHeardInTheOrderItChanged(t *testing.T) {
	f := newFixture(t)
	ch, detach := f.s.hub.Subscribe()
	defer detach()
	for round := 0; round < 50; round++ {
		var wg sync.WaitGroup
		for i := range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				why := ""
				if (round+i)%2 == 0 {
					why = "unreadable"
				}
				f.s.noteRead(f.root, why)
			}()
		}
		wg.Wait()
		want := f.s.health().event().GetSourceHealth().GetHealthy()
		var last *pb.Event
		for drained := false; !drained; {
			select {
			case ev := <-ch:
				last = ev
			case <-time.After(10 * time.Millisecond):
				drained = true
			}
		}
		if last != nil && last.GetSourceHealth().GetHealthy() != want {
			t.Fatalf("round %d: the last health heard is healthy=%v, the files' now is %v", round, !want, want)
		}
	}
}
