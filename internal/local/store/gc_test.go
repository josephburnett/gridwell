package store

import (
	"context"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// Destroying a well from the trash must release everything beneath it. An
// exit well inside the subtree stores a qualified child_grid_id, which the
// teardown scans as an integer, so the whole destroy fails and the subtree's
// rows and blobs can never be released.
func TestDestroyWellHoldingAnExitWellReleasesItsSubtree(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	well, err := s.CreateWell(ctx, root, 0, 0, 1, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateExitWell(ctx, well.ChildGridId, 0, 0, 1, 1, remoteChild, "remote", rpc.View{}); err != nil {
		t.Fatal(err)
	}
	txt, err := s.CreateText(ctx, well.ChildGridId, 1, 0, 1, 1, []byte("body under the well"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: well.Id}); err != nil {
			t.Fatalf("delete round %d: %v", i+1, err)
		}
	}
	if blobExists(t, s, txt.BlobId) {
		t.Errorf("text blob %d under a destroyed well survived", txt.BlobId)
	}
	verifyRefcounts(t, s)
}
