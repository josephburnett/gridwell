package store

import (
	"context"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// Destroying a door from the trash must release everything beneath it. An
// door to another namespace inside the subtree stores a qualified child_grid_id, which the
// teardown scans as an integer, so the whole destroy fails and the subtree's
// rows and blobs can never be released.
func TestDestroyDoorHoldingAnDoorToOtherNamespaceReleasesItsSubtree(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	door, err := s.CreateDoor(ctx, root, 0, 0, 1, 1, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateDoorToOtherNamespace(ctx, door.ChildGridId, 0, 0, 1, 1, remoteChild, "remote", false, rpc.View{}); err != nil {
		t.Fatal(err)
	}
	txt, err := s.CreateText(ctx, door.ChildGridId, 1, 0, 1, 1, []byte("body under the door"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: door.Id}); err != nil {
			t.Fatalf("delete round %d: %v", i+1, err)
		}
	}
	if blobExists(t, s, txt.BlobId) {
		t.Errorf("text blob %d under a destroyed door survived", txt.BlobId)
	}
	verifyRefcounts(t, s)
}
