package store

import (
	"context"
	"errors"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
)

// The PlaceTile suite (2026-07-26 redesign): placement is one verb owning one
// fact, id-addressed + version-claimed, with NO descent path anywhere — the
// well-into-own-subtree refusal must come from the store's own ancestor walk,
// where the old MoveTile trusted a client-supplied DestPath membership check.

func placeText(t *testing.T, s *Store, gridID string, x, y int64) *rpc.Tile {
	t.Helper()
	tile, err := s.CreateText(context.Background(), &rpc.CreateTextRequest{
		GridID: gridID, X: x, Y: y, W: 1, H: 1, Data: []byte("body"),
	})
	if err != nil {
		t.Fatalf("create text: %v", err)
	}
	return tile
}

func placeWell(t *testing.T, s *Store, gridID string, x, y int64) *rpc.Tile {
	t.Helper()
	tile, err := s.CreateWell(context.Background(), &rpc.CreateWellRequest{
		GridID: gridID, X: x, Y: y, W: 1, H: 1,
	})
	if err != nil {
		t.Fatalf("create well: %v", err)
	}
	return tile
}

func TestPlaceTileResizeInPlace(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)
	tile := placeText(t, s, root, 0, 0)

	// Growing in place must not collide with the tile's own old footprint.
	got, err := s.PlaceTile(ctx, &rpc.PlaceTileRequest{
		TileID: tile.ID, Version: tile.Version, GridID: root, X: 0, Y: 0, W: 3, H: 2,
	})
	if err != nil {
		t.Fatalf("place: %v", err)
	}
	if got.W != 3 || got.H != 2 || got.GridID != root {
		t.Errorf("placed = grid %s (%d,%d %dx%d), want grid %s 3x2", got.GridID, got.X, got.Y, got.W, got.H, root)
	}
	if got.Version <= tile.Version {
		t.Errorf("placement is a real edit: version %d should exceed %d", got.Version, tile.Version)
	}
}

func TestPlaceTileMoveAndResizeAtOnce(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)
	well := placeWell(t, s, root, 5, 5)
	tile := placeText(t, s, root, 0, 0)

	got, err := s.PlaceTile(ctx, &rpc.PlaceTileRequest{
		TileID: tile.ID, Version: tile.Version, GridID: well.ChildGridID, X: 2, Y: 3, W: 2, H: 2,
	})
	if err != nil {
		t.Fatalf("place: %v", err)
	}
	if got.GridID != well.ChildGridID || got.X != 2 || got.Y != 3 || got.W != 2 || got.H != 2 {
		t.Errorf("placed = grid %s (%d,%d %dx%d), want child grid %s (2,3 2x2)",
			got.GridID, got.X, got.Y, got.W, got.H, well.ChildGridID)
	}

	// The source grid no longer lists it; the destination does.
	src, err := s.GetGrid(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range src.Tiles {
		if tl.ID == tile.ID {
			t.Error("tile still listed in source grid after cross-grid placement")
		}
	}
	dst, err := s.GetGrid(ctx, well.ChildGridID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tl := range dst.Tiles {
		found = found || tl.ID == tile.ID
	}
	if !found {
		t.Error("tile not listed in destination grid after cross-grid placement")
	}
}

func TestPlaceTileOverlapRefused(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)
	a := placeText(t, s, root, 0, 0)
	_ = a
	b := placeText(t, s, root, 5, 0)

	_, err := s.PlaceTile(ctx, &rpc.PlaceTileRequest{
		TileID: b.ID, Version: b.Version, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if !errors.Is(err, ErrOverlap) {
		t.Fatalf("placing onto an occupied cell: got %v, want ErrOverlap", err)
	}
}

func TestPlaceTileVersionConflict(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)
	tile := placeText(t, s, root, 0, 0)

	_, err := s.PlaceTile(ctx, &rpc.PlaceTileRequest{
		TileID: tile.ID, Version: tile.Version + 41, GridID: root, X: 1, Y: 1, W: 1, H: 1,
	})
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale claim: got %v, want ErrVersionConflict", err)
	}
}

// TestPlaceTileCycleRefusedWithoutPath is the fails-before test for the
// server-derived cycle check: the old MoveTile detected a well moving into
// its own subtree ONLY via the client-supplied DestPath — with no path on
// the wire, the store itself must refuse, at any depth.
func TestPlaceTileCycleRefusedWithoutPath(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)
	wellA := placeWell(t, s, root, 0, 0)
	// wellB lives INSIDE wellA's child grid; its own child is a grandchild of A.
	wellB := placeWell(t, s, wellA.ChildGridID, 0, 0)

	// Into its own child grid: refused.
	if _, err := s.PlaceTile(ctx, &rpc.PlaceTileRequest{
		TileID: wellA.ID, Version: wellA.Version, GridID: wellA.ChildGridID, X: 3, Y: 3, W: 1, H: 1,
	}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("well into own child: got %v, want ErrInvalidArgument", err)
	}
	// Into a grandchild grid: refused (the walk crosses two levels).
	if _, err := s.PlaceTile(ctx, &rpc.PlaceTileRequest{
		TileID: wellA.ID, Version: wellA.Version, GridID: wellB.ChildGridID, X: 3, Y: 3, W: 1, H: 1,
	}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("well into grandchild: got %v, want ErrInvalidArgument", err)
	}
	// The inner well hoisted OUT to the root is legal (no cycle upward).
	if _, err := s.PlaceTile(ctx, &rpc.PlaceTileRequest{
		TileID: wellB.ID, Version: wellB.Version, GridID: root, X: 7, Y: 7, W: 1, H: 1,
	}); err != nil {
		t.Fatalf("hoisting the inner well out: %v", err)
	}
}

// An exit well's child grid belongs to another plugin — no local subtree, so
// placement anywhere local is legal and the walk must not trip on the
// qualified (non-numeric) child_grid_id.
func TestPlaceTileExitWellHasNoLocalSubtree(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)
	interior := placeWell(t, s, root, 0, 0)
	exit, err := s.CreateExitWell(ctx, root, 3, 3, 1, 1,
		"aabbccddaabbccddaabbccddaabbccdd/7", "mounted", 0, 0, 0)
	if err != nil {
		t.Fatalf("create exit well: %v", err)
	}

	if _, err := s.PlaceTile(ctx, &rpc.PlaceTileRequest{
		TileID: exit.ID, Version: exit.Version, GridID: interior.ChildGridID, X: 0, Y: 0, W: 1, H: 1,
	}); err != nil {
		t.Fatalf("placing an exit well into an interior grid: %v", err)
	}
}
