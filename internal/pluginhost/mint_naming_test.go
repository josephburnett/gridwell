package pluginhost_test

import (
	"context"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
)

// A grid keeps its name after its well is touched. The touch mints rows — the
// well's and its child grid's — but a row is storage, not a name: the well the
// next listing answers must keep naming its child grid by the address, and
// GetGrid must answer that grid under the id the well carries. When the mint
// leaked the child grid's ROW id into the listing instead, every descent into
// a previously-touched well died quietly: the client fetched the row id, was
// answered a grid named by the address, cached it under the answered name, and
// looked it up forever under the asked one — a pane stuck on "loading", with
// nothing but 200s on the wire. That is why no gate caught it: every e2e
// descent was a FIRST descent, and the mismatch only exists on the read after
// the mint.
//
// This crosses the production seam — adapter, registry, server, wire client —
// because the two halves of the contract live on opposite sides: the adapter
// names the child grid, the client resolves a frame by that name.
func TestATouchedWellsChildGridKeepsItsName(t *testing.T) {
	cl := pluginNode(t, seedTree(t))
	ctx := context.Background()

	pl, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rootGrid := pl.Plugins[0].RootGridID

	wellByName := func(name string) rpc.Tile {
		t.Helper()
		g, err := cl.GetGrid(ctx, rootGrid)
		if err != nil {
			t.Fatal(err)
		}
		for _, tile := range g.Tiles {
			if tile.AltText == name && tile.Kind == rpc.KindWell {
				return tile
			}
		}
		t.Fatalf("no well %q in the root grid", name)
		return rpc.Tile{}
	}

	before := wellByName("sub")
	if before.ChildGridID == "" {
		t.Fatal("the untouched well names no child grid")
	}

	// The touch: a placement is a durable fact, so it mints the well's row and
	// its child grid's. Exactly what descending into a week and leaving a
	// framing behind does.
	if _, err := cl.PlaceTile(ctx, &rpc.PlaceTileRequest{TileID: before.ID, X: 5, Y: 2, W: 1, H: 1}); err != nil {
		t.Fatal(err)
	}

	after := wellByName("sub")
	if after.ChildGridID != before.ChildGridID {
		t.Fatalf("the touch renamed the well's child grid: %q, was %q — a grid keeps its name for good",
			after.ChildGridID, before.ChildGridID)
	}

	// And the name answers: the grid the well names is the grid GetGrid
	// answers under that very id, which is what the client's cache keys by.
	child, err := cl.GetGrid(ctx, after.ChildGridID)
	if err != nil {
		t.Fatal(err)
	}
	if child.Grid.ID != after.ChildGridID {
		t.Fatalf("GetGrid(%q) answered a grid named %q; the asked name must be the answered name",
			after.ChildGridID, child.Grid.ID)
	}
}
