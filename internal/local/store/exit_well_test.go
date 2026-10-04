package store

import (
	"context"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
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

// An exit well points at another plugin's grid through a qualified
// child_grid_id, and the store treats that child as a shared reference, never
// a thing it owns: clone copies the reference, delete drops only the
// reference, move preserves it.
const remoteChild = "remote-uuid/9"

func TestCloneExitWellSharesReferenceNoNewGrid(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	ew, err := s.CreateExitWell(ctx, root, 0, 0, 1, 1, remoteChild, "remote", rpc.View{})
	if err != nil {
		t.Fatalf("create exit well: %v", err)
	}
	before := gridRowCount(t, s)

	clone, err := s.CloneTile(ctx, &gridwellv1.CloneTileRequest{
		TileId:     ew.Id,
		DestGridId: root, X: 2, Y: 0,
	})
	if err != nil {
		t.Fatalf("clone exit well: %v", err)
	}
	// The clone is a distinct row but carries the SAME qualified child verbatim
	// — a deep copy would have re-pointed it at a fresh local grid.
	if clone.Id == ew.Id {
		t.Error("clone reused the source row id")
	}
	if clone.ChildGridId != remoteChild {
		t.Errorf("clone child = %q, want the shared reference %q", clone.ChildGridId, remoteChild)
	}
	if after := gridRowCount(t, s); after != before {
		t.Errorf("clone of an exit well created %d local grid(s); the far grid is shared, not copied", after-before)
	}
	verifyRefcounts(t, s)
}

func TestDeleteExitWellDropsReferenceOnly(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	// An interior well alongside it, whose LOCAL child grid must survive the
	// exit-well delete untouched.
	interior, err := s.CreateWell(ctx, root, 5, 5, 1, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	ew, err := s.CreateExitWell(ctx, root, 0, 0, 1, 1, remoteChild, "remote", rpc.View{})
	if err != nil {
		t.Fatal(err)
	}
	primeTrash(t, s) // count the delete, not first-use trash minting
	before := gridRowCount(t, s)

	hardDelete(t, s, ew.Id)
	// No local grid was torn down (the well's own grid count drops by one only
	// for an interior well; an exit well owns none).
	if after := gridRowCount(t, s); after != before {
		t.Errorf("deleting an exit well removed %d local grid(s); it owns none", before-after)
	}
	// The interior well's local child grid is still readable.
	if _, err := s.GetGrid(ctx, interior.ChildGridId); err != nil {
		t.Errorf("interior well's child grid was collaterally damaged: %v", err)
	}
	verifyRefcounts(t, s)
}

func TestMoveExitWellPreservesReference(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	ew, err := s.CreateExitWell(ctx, root, 0, 0, 1, 1, remoteChild, "remote", rpc.View{})
	if err != nil {
		t.Fatal(err)
	}
	moved, err := s.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{
		TileId: ew.Id,
		GridId: root, X: 3, Y: 3, W: ew.W, H: ew.H,
	})
	if err != nil {
		t.Fatalf("move exit well: %v", err)
	}
	if moved.ChildGridId != remoteChild {
		t.Errorf("moved exit well child = %q, want %q", moved.ChildGridId, remoteChild)
	}
	if moved.X != 3 || moved.Y != 3 {
		t.Errorf("moved to (%d,%d), want (3,3)", moved.X, moved.Y)
	}
	verifyRefcounts(t, s)
}
