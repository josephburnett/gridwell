package store

import (
	"context"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// gridRowCount returns how many grid rows exist — the witness that a door to another namespace
// operation did NOT materialize (copy) or tear down (delete) a local grid.
func gridRowCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM grids`).Scan(&n); err != nil {
		t.Fatalf("count grids: %v", err)
	}
	return n
}

// A door to another namespace points at another plugin's grid through a qualified
// child_grid_id, and the store treats that child as a shared link, never
// a thing it owns: copy copies the link, delete drops only the
// link, move preserves it.
const remoteChild = "remote-uuid/9"

func TestCopyDoorToOtherNamespaceSharesLinkNoNewGrid(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	ew, err := s.CreateDoorToOtherNamespace(ctx, root, 0, 0, 1, 1, remoteChild, "remote", false, rpc.View{})
	if err != nil {
		t.Fatalf("create door to another namespace: %v", err)
	}
	before := gridRowCount(t, s)

	cp, err := s.CopyTile(ctx, &gridwellv1.CopyTileRequest{
		TileId:     ew.Id,
		DestGridId: root, X: 2, Y: 0,
	})
	if err != nil {
		t.Fatalf("copy door to another namespace: %v", err)
	}
	// The copy is a distinct row but carries the SAME qualified child verbatim
	// — a deep copy would have re-pointed it at a fresh local grid.
	if cp.Id == ew.Id {
		t.Error("copy reused the source row id")
	}
	if cp.ChildGridId != remoteChild {
		t.Errorf("copy child = %q, want the shared link %q", cp.ChildGridId, remoteChild)
	}
	if after := gridRowCount(t, s); after != before {
		t.Errorf("copy of a door to another namespace created %d local grid(s); the far grid is shared, not copied", after-before)
	}
	verifyRefcounts(t, s)
}

func TestDeleteDoorToOtherNamespaceDropsLinkOnly(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	// An interior door alongside it, whose LOCAL child grid must survive the
	// door to another namespace delete untouched.
	interior, err := s.CreateDoor(ctx, root, 5, 5, 1, 1, "", false)
	if err != nil {
		t.Fatal(err)
	}
	ew, err := s.CreateDoorToOtherNamespace(ctx, root, 0, 0, 1, 1, remoteChild, "remote", false, rpc.View{})
	if err != nil {
		t.Fatal(err)
	}
	primeTrash(t, s) // count the delete, not first-use trash minting
	before := gridRowCount(t, s)

	hardDelete(t, s, ew.Id)
	// No local grid was torn down (the door's own grid count drops by one only
	// for an interior door; a door to another namespace owns none).
	if after := gridRowCount(t, s); after != before {
		t.Errorf("deleting a door to another namespace removed %d local grid(s); it owns none", before-after)
	}
	// The interior door's local child grid is still readable.
	if _, err := s.GetGrid(ctx, interior.ChildGridId); err != nil {
		t.Errorf("interior door's child grid was collaterally damaged: %v", err)
	}
	verifyRefcounts(t, s)
}

func TestMoveDoorToOtherNamespacePreservesLink(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	ew, err := s.CreateDoorToOtherNamespace(ctx, root, 0, 0, 1, 1, remoteChild, "remote", false, rpc.View{})
	if err != nil {
		t.Fatal(err)
	}
	moved, err := s.MoveTile(ctx, &gridwellv1.MoveTileRequest{
		TileId: ew.Id,
		GridId: root, X: 3, Y: 3, W: ew.W, H: ew.H,
	})
	if err != nil {
		t.Fatalf("move door to another namespace: %v", err)
	}
	if moved.ChildGridId != remoteChild {
		t.Errorf("moved door to another namespace child = %q, want %q", moved.ChildGridId, remoteChild)
	}
	if moved.X != 3 || moved.Y != 3 {
		t.Errorf("moved to (%d,%d), want (3,3)", moved.X, moved.Y)
	}
	verifyRefcounts(t, s)
}
