package server_test

import (
	"bytes"
	"context"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/clientsync"
)

// Two clients read a thing the user made at one version; the first writes,
// and the second's write, still claiming what it read, is refused with the
// conflict the client reconciles from, the first's write standing. A thing on
// a far node behaves the same as one at home.

// staleWhere is each place a tile can live, spelled from node me.
func staleWhere(m *mesh) map[string]string {
	return map[string]string{
		"home":     m.home(meNode, meNode),
		"far node": m.home(meNode, aNode, "toa"),
	}
}

func staleMesh(t *testing.T) *mesh {
	return newMesh(t, []string{meNode, aNode}, []meshEdge{{meNode, "toa", aNode}})
}

func wantConflict(t *testing.T, what string, err error) {
	t.Helper()
	if o := clientsync.Of(err); o != clientsync.OutcomeConflict {
		t.Fatalf("%s: got %v (outcome %v), want the version conflict", what, err, o)
	}
}

func TestAStaleLayoutWriteIsRefusedAndTheNewerArrangementStands(t *testing.T) {
	ctx := context.Background()
	m := staleMesh(t)
	for where, grid := range staleWhere(m) {
		t.Run(where, func(t *testing.T) {
			cl := m.nodes[meNode].cl
			pt, err := cl.CreateWithContent(ctx, &gridwellv1.CreateTileRequest{GridId: grid,
				Tile: &gridwellv1.Tile{Kind: rpc.KindPane, X: 5, Y: 0, W: 1, H: 1}},
				[]byte(`{"v":1,"root":{"pane":{"id":"p1","zoom":1}},"focus":"p1"}`))
			if err != nil {
				t.Fatal(err)
			}
			_, _, first, err := cl.ReadContent(ctx, pt.Id)
			if err != nil {
				t.Fatal(err)
			}
			second := first // the other window read the same arrangement
			newer := []byte(`{"v":1,"root":{"split":{"dir":"v","ratio":0.5,"a":{"pane":{"id":"p1","zoom":1}},"b":{"pane":{"id":"p2","zoom":1}}}},"focus":"p2"}`)
			landed, err := cl.WriteContent(ctx, pt.Id, first, newer)
			if err != nil {
				t.Fatalf("first writer: %v", err)
			}
			if landed.Version != first.Version+1 {
				t.Fatalf("version %d -> %d, want a bump", first.Version, landed.Version)
			}
			_, err = cl.WriteContent(ctx, pt.Id, second, []byte(`{"v":1,"root":{"pane":{"id":"p9","zoom":1}},"focus":"p9"}`))
			wantConflict(t, "stale layout write", err)
			data, _, basis, err := cl.ReadContent(ctx, pt.Id)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(data, newer) || basis.Version != landed.Version {
				t.Fatalf("on disk %q at %d, want the first writer's arrangement at %d", data, basis.Version, landed.Version)
			}
		})
	}
}

func placementOf(t *gridwellv1.Tile) *gridwellv1.Placement {
	return &gridwellv1.Placement{GridId: t.GridId, X: t.X, Y: t.Y, W: t.W, H: t.H}
}

func TestAStalePlacementIsRefusedAndTheFirstMoveStands(t *testing.T) {
	ctx := context.Background()
	m := staleMesh(t)
	for where, grid := range staleWhere(m) {
		t.Run(where, func(t *testing.T) {
			cl := m.nodes[meNode].cl
			tile := m.text(ctx, meNode, grid, 20, "# placed")
			seen := placementOf(m.tileIn(ctx, meNode, grid, tile.Id)) // both windows read it here
			moved, err := cl.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{TileId: tile.Id, GridId: grid,
				X: 22, Y: 0, W: 1, H: 1, Claim: seen})
			if err != nil {
				t.Fatalf("first move: %v", err)
			}
			if moved.Version != tile.Version {
				t.Fatalf("version %d -> %d: a move bumps no version", tile.Version, moved.Version)
			}
			_, err = cl.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{TileId: tile.Id, GridId: grid,
				X: 24, Y: 0, W: 1, H: 1, Claim: seen})
			wantConflict(t, "stale move", err)
			if got := m.tileIn(ctx, meNode, grid, tile.Id); got.X != 22 {
				t.Fatalf("x = %d, want the first move's 22 standing", got.X)
			}
		})
	}
}

func TestAStaleDeleteIsRefusedAndTheTileStands(t *testing.T) {
	ctx := context.Background()
	m := staleMesh(t)
	for where, grid := range staleWhere(m) {
		t.Run(where, func(t *testing.T) {
			cl := m.nodes[meNode].cl
			tile := m.text(ctx, meNode, grid, 30, "# kept")
			seen := m.tileIn(ctx, meNode, grid, tile.Id) // both windows read it here
			edited, err := cl.WriteContent(ctx, tile.Id, rpc.BasisOf(seen), []byte("# kept, edited"))
			if err != nil {
				t.Fatalf("first window's edit: %v", err)
			}
			_, err = cl.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: tile.Id,
				ClaimVersion: seen.Version, Claim: placementOf(seen)})
			wantConflict(t, "delete over an edit it never saw", err)
			if got := m.tileIn(ctx, meNode, grid, tile.Id); got.Version != edited.Version {
				t.Fatalf("version %d, want the edited %d standing", got.Version, edited.Version)
			}
			if _, err := cl.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: tile.Id,
				ClaimVersion: edited.Version, Claim: placementOf(seen)}); err != nil {
				t.Fatalf("delete of what the writer saw: %v", err)
			}
		})
	}
}

// Window one sees the page frozen by another and freezes it again over an
// unfreeze it never saw: refused, the unfreeze standing.
func TestAStaleFreezeIsRefusedAndTheNewerFreezeStateStands(t *testing.T) {
	ctx := context.Background()
	m := staleMesh(t)
	for where, grid := range staleWhere(m) {
		t.Run(where, func(t *testing.T) {
			cl := m.nodes[meNode].cl
			page, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: grid,
				Tile: &gridwellv1.Tile{Kind: rpc.KindURL, X: 40, Y: 0, W: 1, H: 1, UrlString: "https://example.com/"}})
			if err != nil {
				t.Fatal(err)
			}
			no, yes := false, true
			if _, err := cl.SetFrozen(ctx, page.Id, true, no); err != nil {
				t.Fatalf("freeze: %v", err)
			}
			// Window one read it frozen here; window two unfreezes it.
			if _, err := cl.SetFrozen(ctx, page.Id, false, yes); err != nil {
				t.Fatalf("unfreeze: %v", err)
			}
			_, err = cl.SetFrozen(ctx, page.Id, true, yes)
			wantConflict(t, "freeze over an unfreeze it never saw", err)
			if got := m.tileIn(ctx, meNode, grid, page.Id); got.UrlFrozen || got.Version != page.Version {
				t.Fatalf("frozen=%v version %d, want the unfreeze standing at version %d", got.UrlFrozen, got.Version, page.Version)
			}
		})
	}
}
