package remote

import (
	"path/filepath"
	"strconv"
	"testing"
	"time"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// A Subscribe stream that stalls must not lose changes: the hub coalesces
// per entity (a newer change to the same tile replaces the older
// undelivered one) and never drops a DISTINCT one. The old 64-slot
// channel dropped the 65th tile on the floor — a pane stayed stale until
// some unrelated event touched the same grid.
func TestHubNeverDropsDistinctTilesForAStalledSubscriber(t *testing.T) {
	db, err := OpenDB(filepath.Join(t.TempDir(), "remote.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := newTestServer(t, db)
	events, unsub := s.hub.Subscribe()
	t.Cleanup(unsub)

	const n = 65
	for i := 0; i < n; i++ {
		s.hub.Publish(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{TileChanged: &gridwellv1.TileChanged{
			Tile: &gridwellv1.Tile{Id: "t" + strconv.Itoa(i), GridId: "g"},
		}}})
	}
	seen := map[string]bool{}
	deadline := time.After(3 * time.Second)
	for len(seen) < n {
		select {
		case ev := <-events:
			seen[ev.GetTileChanged().GetTile().GetId()] = true
		case <-deadline:
			t.Fatalf("drained %d of %d distinct tile changes — the rest were dropped", len(seen), n)
		}
	}
}
