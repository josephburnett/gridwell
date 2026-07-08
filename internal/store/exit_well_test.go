package store

import (
	"context"
	"testing"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// gridRowCount returns how many grid rows exist — the witness that an exit-well
// operation did NOT materialize (clone) or tear down (delete) a local grid.
func gridRowCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM grids`).Scan(&n); err != nil {
		t.Fatalf("count grids: %v", err)
	}
	return n
}

// An exit well points at a grid owned by ANOTHER plugin via a qualified
// "<uuid>/<local>" child_grid_id. The store must treat that child as a shared
// reference, never a thing it owns: clone copies the reference (no new grid),
// delete drops only the reference (no remote teardown), move preserves it.
// These are the exit-well faces of the primary rule — the remote grid stays
// exactly as it was because nothing the user did here touched it.
const remoteChild = "remote-uuid/9"

func TestCloneExitWellSharesReferenceNoNewGrid(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	ew, err := s.CreateExitWell(ctx, rpc.Path{}, root, 0, 0, 1, 1, remoteChild, "remote", 0, 0, 0)
	if err != nil {
		t.Fatalf("create exit well: %v", err)
	}
	before := gridRowCount(t, s)

	clone, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		Path: rpc.Path{}, TileID: ew.ID, Version: ew.Version,
		DestGridID: root, DestPath: rpc.Path{}, X: 2, Y: 0,
	})
	if err != nil {
		t.Fatalf("clone exit well: %v", err)
	}
	// The clone is a distinct row but carries the SAME qualified child verbatim
	// — a deep copy would have re-pointed it at a fresh local grid.
	if clone.ID == ew.ID {
		t.Error("clone reused the source row id")
	}
	if clone.ChildGridID != remoteChild {
		t.Errorf("clone child = %q, want the shared reference %q", clone.ChildGridID, remoteChild)
	}
	if after := gridRowCount(t, s); after != before {
		t.Errorf("clone of an exit well created %d local grid(s); the remote is shared, not copied", after-before)
	}
	verifyRefcounts(t, s)
}

func TestDeleteExitWellDropsReferenceOnly(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	// An interior well alongside it, whose LOCAL child grid must survive the
	// exit-well delete untouched.
	interior, err := s.CreateWell(ctx, &rpc.CreateWellRequest{Path: rpc.Path{}, GridID: root, X: 5, Y: 5, W: 1, H: 1})
	if err != nil {
		t.Fatal(err)
	}
	ew, err := s.CreateExitWell(ctx, rpc.Path{}, root, 0, 0, 1, 1, remoteChild, "remote", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	before := gridRowCount(t, s)

	if err := s.DeleteTile(ctx, &rpc.DeleteTileRequest{Path: rpc.Path{}, TileID: ew.ID, Version: ew.Version}); err != nil {
		t.Fatalf("delete exit well: %v", err)
	}
	// No local grid was torn down (the well's own grid count drops by one only
	// for an interior well; an exit well owns none).
	if after := gridRowCount(t, s); after != before {
		t.Errorf("deleting an exit well removed %d local grid(s); it owns none", before-after)
	}
	// The interior well's local child grid is still readable.
	if _, err := s.GetGrid(ctx, interior.ChildGridID); err != nil {
		t.Errorf("interior well's child grid was collaterally damaged: %v", err)
	}
	verifyRefcounts(t, s)
}

func TestMoveExitWellPreservesReference(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	ew, err := s.CreateExitWell(ctx, rpc.Path{}, root, 0, 0, 1, 1, remoteChild, "remote", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	moved, err := s.MoveTile(ctx, &rpc.MoveTileRequest{
		Path: rpc.Path{}, TileID: ew.ID, Version: ew.Version,
		DestGridID: root, DestPath: rpc.Path{}, X: 3, Y: 3,
	})
	if err != nil {
		t.Fatalf("move exit well: %v", err)
	}
	if moved.ChildGridID != remoteChild {
		t.Errorf("moved exit well child = %q, want %q", moved.ChildGridID, remoteChild)
	}
	if moved.X != 3 || moved.Y != 3 {
		t.Errorf("moved to (%d,%d), want (3,3)", moved.X, moved.Y)
	}
	verifyRefcounts(t, s)
}
