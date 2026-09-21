package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// Placement is one verb owning one fact, id-addressed with no descent path, so
// the well-into-own-subtree refusal comes from the store's own ancestor walk.
// The overlap refusal, not a claim, is what protects the grid.

func placeText(t *testing.T, s *Store, gridID string, x, y int64) *gridwellv1.Tile {
	t.Helper()
	tile, err := s.CreateText(context.Background(), gridID, x, y, 1, 1, []byte("body"))
	if err != nil {
		t.Fatalf("create text: %v", err)
	}
	return tile
}

func placeWell(t *testing.T, s *Store, gridID string, x, y int64) *gridwellv1.Tile {
	t.Helper()
	tile, err := s.CreateWell(context.Background(), gridID, x, y, 1, 1, "")
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
	got, err := s.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{
		TileId: tile.Id, GridId: root, X: 0, Y: 0, W: 3, H: 2,
	})
	if err != nil {
		t.Fatalf("place: %v", err)
	}
	if got.W != 3 || got.H != 2 || got.GridId != root {
		t.Errorf("placed = grid %s (%d,%d %dx%d), want grid %s 3x2", got.GridId, got.X, got.Y, got.W, got.H, root)
	}
	// Placement is layout, not content: the version stays put
	// (version_rule_test.go owns the whole rule).
	if got.Version != tile.Version {
		t.Errorf("placement moved the version %d -> %d; layout does not bump", tile.Version, got.Version)
	}
}

func TestPlaceTileMoveAndResizeAtOnce(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)
	well := placeWell(t, s, root, 5, 5)
	tile := placeText(t, s, root, 0, 0)

	got, err := s.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{
		TileId: tile.Id, GridId: well.ChildGridId, X: 2, Y: 3, W: 2, H: 2,
	})
	if err != nil {
		t.Fatalf("place: %v", err)
	}
	if got.GridId != well.ChildGridId || got.X != 2 || got.Y != 3 || got.W != 2 || got.H != 2 {
		t.Errorf("placed = grid %s (%d,%d %dx%d), want child grid %s (2,3 2x2)",
			got.GridId, got.X, got.Y, got.W, got.H, well.ChildGridId)
	}

	// The source grid does not list it; the destination does.
	src, err := s.GetGrid(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range src.Tiles {
		if tl.Id == tile.Id {
			t.Error("tile still listed in source grid after cross-grid placement")
		}
	}
	dst, err := s.GetGrid(ctx, well.ChildGridId)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tl := range dst.Tiles {
		found = found || tl.Id == tile.Id
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

	_, err := s.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{
		TileId: b.Id, GridId: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if !errors.Is(err, ErrOverlap) {
		t.Fatalf("placing onto an occupied cell: got %v, want ErrOverlap", err)
	}
}

// A drag is an explicit act on a tile the user can see, so a version that
// moved under it must not cost the user the move.
func TestPlaceTileIgnoresStaleClaim(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)
	tile := placeText(t, s, root, 0, 0)

	got, err := s.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{
		TileId: tile.Id, GridId: root, X: 1, Y: 1, W: 1, H: 1,
	})
	if err != nil {
		t.Fatalf("stale claim must be accepted: %v", err)
	}
	if got.X != 1 || got.Y != 1 {
		t.Errorf("placed at (%d,%d), want (1,1)", got.X, got.Y)
	}
	// The grid is still protected: an overlapping place is refused whatever
	// the claim says.
	other := placeText(t, s, root, 5, 5)
	if _, err := s.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{
		TileId: other.Id, GridId: root, X: 1, Y: 1, W: 1, H: 1,
	}); !errors.Is(err, ErrOverlap) {
		t.Fatalf("overlap with a stale claim: got %v, want ErrOverlap", err)
	}
}

// With no path on the wire, the store itself must refuse a well moving into
// its own subtree, at any depth.
func TestPlaceTileCycleRefusedWithoutPath(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)
	wellA := placeWell(t, s, root, 0, 0)
	// wellB lives INSIDE wellA's child grid; its own child is a grandchild of A.
	wellB := placeWell(t, s, wellA.ChildGridId, 0, 0)

	// Into its own child grid: refused.
	if _, err := s.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{
		TileId: wellA.Id, GridId: wellA.ChildGridId, X: 3, Y: 3, W: 1, H: 1,
	}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("well into own child: got %v, want ErrInvalidArgument", err)
	}
	// Into a grandchild grid: refused (the walk crosses two levels).
	if _, err := s.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{
		TileId: wellA.Id, GridId: wellB.ChildGridId, X: 3, Y: 3, W: 1, H: 1,
	}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("well into grandchild: got %v, want ErrInvalidArgument", err)
	}
	// The inner well hoisted OUT to the root is legal (no cycle upward).
	if _, err := s.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{
		TileId: wellB.Id, GridId: root, X: 7, Y: 7, W: 1, H: 1,
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
		"aabbccddaabbccddaabbccddaabbccdd/7", "mounted", rpc.Framing{})
	if err != nil {
		t.Fatalf("create exit well: %v", err)
	}

	if _, err := s.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{
		TileId: exit.Id, GridId: interior.ChildGridId, X: 0, Y: 0, W: 1, H: 1,
	}); err != nil {
		t.Fatalf("placing an exit well into an interior grid: %v", err)
	}
}

// No verb can make a child_grid_id cycle, but a corrupted file can hold one,
// and the placement walk must answer rather than spin forever.
func TestPlaceTileAncestryCycleAnswers(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)
	moving := placeWell(t, s, root, 0, 0)
	p := placeWell(t, s, root, 1, 0)
	q := placeWell(t, s, root, 2, 0)

	// Hang each of the two wells under the other's child grid, so walking up
	// from either grid never reaches a root.
	for _, swap := range [][2]string{{p.Id, q.ChildGridId}, {q.Id, p.ChildGridId}} {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE tiles SET grid_id = ? WHERE id = ?`, swap[1], swap[0]); err != nil {
			t.Fatalf("seed cycle: %v", err)
		}
	}

	done := make(chan error, 1)
	go func() {
		_, err := s.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{
			TileId: moving.Id, GridId: p.ChildGridId, X: 3, Y: 3, W: 1, H: 1,
		})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "ancestry deeper") {
			t.Fatalf("place into a cyclic grid: got %v, want the capped-ancestry error", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("PlaceTile hung walking a cyclic ancestry chain")
	}
}
