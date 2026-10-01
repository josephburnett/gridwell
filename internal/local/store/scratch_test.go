package store

import (
	"context"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// TestScratchGridStableAndDistinct: the scratch grid id is created once and
// returned verbatim thereafter, and it is a different grid from the root — so
// ephemeral url tiles never land on the user's home grid.
func TestScratchGridStableAndDistinct(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	first, err := s.ScratchGridID(ctx)
	if err != nil {
		t.Fatalf("ScratchGridID: %v", err)
	}
	if first == "" {
		t.Fatal("scratch grid id is empty")
	}
	again, err := s.ScratchGridID(ctx)
	if err != nil {
		t.Fatalf("ScratchGridID (second call): %v", err)
	}
	if again != first {
		t.Errorf("scratch grid id not stable: %q then %q", first, again)
	}
	root := rootID(t, s)
	if first == root {
		t.Errorf("scratch grid id %q must differ from root %q", first, root)
	}
}

// TestScratchGridHoldsEphemeralURL: a url tile created in the scratch grid is
// readable there (so descent + autocomplete can find it) but never appears on
// the root grid (so it doesn't litter the home space).
func TestScratchGridHoldsEphemeralURL(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)

	tile, err := s.CreateScratchURL(ctx, "https://example.com/ephemeral")
	if err != nil {
		t.Fatalf("create ephemeral url: %v", err)
	}
	scratch, err := s.ScratchGridID(ctx)
	if err != nil {
		t.Fatalf("ScratchGridID: %v", err)
	}

	got, err := s.GetGrid(ctx, scratch)
	if err != nil {
		t.Fatalf("GetGrid scratch: %v", err)
	}
	if len(got.Tiles) != 1 || got.Tiles[0].Id != tile.Id || got.Tiles[0].UrlString != "https://example.com/ephemeral" {
		t.Errorf("scratch grid tiles = %+v, want the one ephemeral url", got.Tiles)
	}

	home, err := s.GetGrid(ctx, root)
	if err != nil {
		t.Fatalf("GetGrid root: %v", err)
	}
	if len(home.Tiles) != 0 {
		t.Errorf("root grid gained %d tile(s); ephemeral url must stay off it", len(home.Tiles))
	}

	// A second visit must succeed even though it lands on the same (0,0) cell:
	// the scratch grid runs no overlap check (it is never rendered).
	if _, err := s.CreateScratchURL(ctx, "https://example.com/second"); err != nil {
		t.Fatalf("second ephemeral url (same cell) should not overlap-fail: %v", err)
	}
	got, err = s.GetGrid(ctx, scratch)
	if err != nil {
		t.Fatalf("GetGrid scratch (after 2nd): %v", err)
	}
	if len(got.Tiles) != 2 {
		t.Errorf("scratch grid has %d tiles, want 2 accumulated visits", len(got.Tiles))
	}
}

// A scratch-grid tile is off-grid, so no descent path can reach it and
// checkPathLeaf must treat the scratch grid as its own leaf. Otherwise every
// mutation on an ephemeral tile fails "descent path is invalid" and
// delete-on-ascent is impossible.
func TestScratchTileMutationsNeedNoPath(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	tile, err := s.CreateScratchURL(ctx, "https://example.com/eph")
	if err != nil {
		t.Fatalf("create ephemeral url: %v", err)
	}
	// A content writeback with an EMPTY path succeeds.
	if _, err := s.SetURLState(ctx, tile.Id, nil, "", ""); err != nil {
		t.Fatalf("SetURLState on a scratch tile: %v", err)
	}
	// And so does delete.
	if err := s.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: tile.Id}); err != nil {
		t.Fatalf("DeleteTile on a scratch tile: %v", err)
	}
	if _, err := s.GetTile(ctx, tile.Id); err == nil {
		t.Fatal("scratch tile still readable after delete")
	}
}

// TestCreateScratchShell: the ephemeral-shell twin of
// CreateScratchURL — a shell tile created off-grid in the scratch grid,
// deletable with no path.
func TestCreateScratchShell(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)

	tile, err := s.CreateScratchShell(ctx)
	if err != nil {
		t.Fatalf("CreateScratchShell: %v", err)
	}
	if tile.Kind != rpc.KindShell {
		t.Fatalf("kind = %q, want shell", tile.Kind)
	}
	scratch, _ := s.ScratchGridID(ctx)
	if tile.GridId != scratch {
		t.Fatalf("tile grid = %q, want scratch %q", tile.GridId, scratch)
	}
	rootGrid, err := s.GetGrid(ctx, root)
	if err != nil {
		t.Fatalf("GetGrid(root): %v", err)
	}
	for _, rt := range rootGrid.Tiles {
		if rt.Id == tile.Id {
			t.Fatal("ephemeral shell leaked onto the root grid")
		}
	}
	if err := s.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: tile.Id}); err != nil {
		t.Fatalf("DeleteTile: %v", err)
	}
}
