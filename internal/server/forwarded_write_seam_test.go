package server_test

import (
	"context"
	"testing"
	"time"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/sourcecache"
)

// A grid read the node answers after it acknowledged a write it forwarded to
// a far node holds that write, from the cache in front of the real
// transport, with no far event to fold: the write's own answer is folded
// before it is acknowledged. Rows held by the cache are read fresh-window
// stale, so every read also races a revalidation.
func TestAReadAfterAForwardedWriteHoldsIt(t *testing.T) {
	m := newMesh(t, []string{"anode01", "bnode01"}, []meshEdge{{From: "anode01", Name: "second", To: "bnode01"}})
	cache, err := sourcecache.Open(t.TempDir() + "/cache.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	a := m.nodes["anode01"]
	restart(a.reg, cache.Front(transportOf(a.reg), sourcecache.Options{Prefetch: true, FreshWindow: time.Millisecond}))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	far := m.home("anode01", "bnode01", "second")
	at := func(id string) *gridwellv1.Tile {
		g, err := a.cl.GetGrid(ctx, far)
		if err != nil {
			t.Fatal(err)
		}
		for _, tl := range g.Tiles {
			if tl.Id == id {
				return tl
			}
		}
		return nil
	}
	at("")
	for i := int64(0); i < 50; i++ {
		tl, err := a.cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: far, Tile: &gridwellv1.Tile{Kind: rpc.KindDoor, X: i, Y: 0, W: 1, H: 1}})
		if err != nil {
			t.Fatal(err)
		}
		if at(tl.Id) == nil {
			t.Fatalf("rep %d: the read after the create lacks %s", i, tl.Id)
		}
		moved, err := a.cl.MoveTile(ctx, &gridwellv1.MoveTileRequest{TileId: tl.Id, GridId: far, X: i, Y: 5, W: 1, H: 1, Claim: rpc.PlacementOf(tl)})
		if err != nil {
			t.Fatal(err)
		}
		if got := at(tl.Id); got == nil || got.Y != 5 {
			t.Fatalf("rep %d: the read after the move has %v", i, got)
		}
		if _, err := a.cl.DeleteTile(ctx, rpc.DeleteOf(moved)); err != nil {
			t.Fatal(err)
		}
		if at(tl.Id) != nil {
			t.Fatalf("rep %d: the read after the delete still has %s", i, tl.Id)
		}
	}
}
