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
// the door-into-own-subtree refusal comes from the store's own ancestor walk.
// The overlap refusal, not a claim, is what protects the grid.

func textAt(t *testing.T, s *Store, gridID string, x, y int64) *gridwellv1.Tile {
	t.Helper()
	tile, err := s.CreateText(context.Background(), gridID, x, y, 1, 1, []byte("body"))
	if err != nil {
		t.Fatalf("create text: %v", err)
	}
	return tile
}

func doorAt(t *testing.T, s *Store, gridID string, x, y int64) *gridwellv1.Tile {
	t.Helper()
	tile, err := s.CreateDoor(context.Background(), gridID, x, y, 1, 1, "")
	if err != nil {
		t.Fatalf("create door: %v", err)
	}
	return tile
}

func TestMoveTileResizeInPlace(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)
	tile := textAt(t, s, root, 0, 0)

	// Growing in place must not collide with the tile's own old footprint.
	got, err := s.MoveTile(ctx, &gridwellv1.MoveTileRequest{
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

func TestMoveTileMoveAndResizeAtOnce(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)
	door := doorAt(t, s, root, 5, 5)
	tile := textAt(t, s, root, 0, 0)

	got, err := s.MoveTile(ctx, &gridwellv1.MoveTileRequest{
		TileId: tile.Id, GridId: door.ChildGridId, X: 2, Y: 3, W: 2, H: 2,
	})
	if err != nil {
		t.Fatalf("place: %v", err)
	}
	if got.GridId != door.ChildGridId || got.X != 2 || got.Y != 3 || got.W != 2 || got.H != 2 {
		t.Errorf("placed = grid %s (%d,%d %dx%d), want child grid %s (2,3 2x2)",
			got.GridId, got.X, got.Y, got.W, got.H, door.ChildGridId)
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
	dst, err := s.GetGrid(ctx, door.ChildGridId)
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

func TestMoveTileOverlapRefused(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)
	a := textAt(t, s, root, 0, 0)
	_ = a
	b := textAt(t, s, root, 5, 0)

	_, err := s.MoveTile(ctx, &gridwellv1.MoveTileRequest{
		TileId: b.Id, GridId: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if !errors.Is(err, ErrOverlap) {
		t.Fatalf("placing onto an occupied cell: got %v, want ErrOverlap", err)
	}
}

// A drag is an explicit act on a tile the user can see, so a version that
// moved under it must not cost the user the move.
func TestMoveTileIgnoresStaleClaim(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)
	tile := textAt(t, s, root, 0, 0)

	got, err := s.MoveTile(ctx, &gridwellv1.MoveTileRequest{
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
	other := textAt(t, s, root, 5, 5)
	if _, err := s.MoveTile(ctx, &gridwellv1.MoveTileRequest{
		TileId: other.Id, GridId: root, X: 1, Y: 1, W: 1, H: 1,
	}); !errors.Is(err, ErrOverlap) {
		t.Fatalf("overlap with a stale claim: got %v, want ErrOverlap", err)
	}
}

// With no path on the wire, the store itself must refuse a door moving into
// its own subtree, at any depth.
func TestMoveTileCycleRefusedWithoutPath(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)
	doorA := doorAt(t, s, root, 0, 0)
	// doorB lives INSIDE doorA's child grid; its own child is a grandchild of A.
	doorB := doorAt(t, s, doorA.ChildGridId, 0, 0)

	// Into its own child grid: refused.
	if _, err := s.MoveTile(ctx, &gridwellv1.MoveTileRequest{
		TileId: doorA.Id, GridId: doorA.ChildGridId, X: 3, Y: 3, W: 1, H: 1,
	}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("door into own child: got %v, want ErrInvalidArgument", err)
	}
	// Into a grandchild grid: refused (the walk crosses two levels).
	if _, err := s.MoveTile(ctx, &gridwellv1.MoveTileRequest{
		TileId: doorA.Id, GridId: doorB.ChildGridId, X: 3, Y: 3, W: 1, H: 1,
	}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("door into grandchild: got %v, want ErrInvalidArgument", err)
	}
	// The inner door hoisted OUT to the root is legal (no cycle upward).
	if _, err := s.MoveTile(ctx, &gridwellv1.MoveTileRequest{
		TileId: doorB.Id, GridId: root, X: 7, Y: 7, W: 1, H: 1,
	}); err != nil {
		t.Fatalf("hoisting the inner door out: %v", err)
	}
}

// The child grid of a door to another namespace belongs to another plugin — no local subtree, so
// placement anywhere local is legal and the walk must not trip on the
// qualified (non-numeric) child_grid_id.
func TestMoveTileDoorToOtherNamespaceHasNoLocalSubtree(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)
	interior := doorAt(t, s, root, 0, 0)
	far, err := s.CreateDoorToOtherNamespace(ctx, root, 3, 3, 1, 1,
		"aabbccddaabbccddaabbccddaabbccdd/7", "far", rpc.View{})
	if err != nil {
		t.Fatalf("create door to another namespace: %v", err)
	}

	if _, err := s.MoveTile(ctx, &gridwellv1.MoveTileRequest{
		TileId: far.Id, GridId: interior.ChildGridId, X: 0, Y: 0, W: 1, H: 1,
	}); err != nil {
		t.Fatalf("placing a door to another namespace into an interior grid: %v", err)
	}
}

// No verb can make a child_grid_id cycle, but a corrupted file can hold one,
// and the placement walk must answer rather than spin forever.
func TestMoveTileAncestryCycleAnswers(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)
	moving := doorAt(t, s, root, 0, 0)
	p := doorAt(t, s, root, 1, 0)
	q := doorAt(t, s, root, 2, 0)

	// Hang each of the two doors under the other's child grid, so walking up
	// from either grid never reaches a root.
	for _, swap := range [][2]string{{p.Id, q.ChildGridId}, {q.Id, p.ChildGridId}} {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE tiles SET grid_id = ? WHERE id = ?`, swap[1], swap[0]); err != nil {
			t.Fatalf("seed cycle: %v", err)
		}
	}

	done := make(chan error, 1)
	go func() {
		_, err := s.MoveTile(ctx, &gridwellv1.MoveTileRequest{
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
		t.Fatal("MoveTile hung walking a cyclic ancestry chain")
	}
}

// A placement claims where the writer saw the tile. A stale one is refused
// with the placement on disk standing; the claim is the state, not its
// history, so a tile moved away and back is where the writer saw it; and a
// tile already where the write puts it is no conflict.
func TestMoveTileClaimsThePlacementItSaw(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)
	tile, err := s.CreateText(ctx, root, 0, 0, 1, 1, []byte("# hi"))
	if err != nil {
		t.Fatal(err)
	}
	seen := &gridwellv1.Placement{GridId: tile.GridId, X: 0, Y: 0, W: 1, H: 1}
	place := func(x int64, claim *gridwellv1.Placement) (*gridwellv1.Tile, error) {
		return s.MoveTile(ctx, &gridwellv1.MoveTileRequest{TileId: tile.Id, GridId: root, X: x, Y: 0, W: 1, H: 1, Claim: claim})
	}
	if _, err := place(3, seen); err != nil {
		t.Fatalf("first writer: %v", err)
	}
	if _, err := place(5, seen); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale placement: got %v, want ErrVersionConflict", err)
	}
	if got, _ := s.GetTile(ctx, tile.Id); got.X != 3 {
		t.Fatalf("x = %d, want the first writer's 3 standing", got.X)
	}
	if _, err := place(3, seen); err != nil {
		t.Fatalf("a stale writer asking for where the tile already is: %v", err)
	}
	if _, err := place(0, &gridwellv1.Placement{GridId: tile.GridId, X: 3, Y: 0, W: 1, H: 1}); err != nil {
		t.Fatalf("move back: %v", err)
	}
	if _, err := place(7, seen); err != nil {
		t.Fatalf("moved away and back is where the writer saw it: %v", err)
	}
	if got, _ := s.GetTile(ctx, tile.Id); got.Version != tile.Version {
		t.Fatalf("version %d -> %d: a placement claims no version and bumps none", tile.Version, got.Version)
	}
}
