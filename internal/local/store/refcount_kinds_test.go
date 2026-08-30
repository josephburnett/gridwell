package store

import (
	"context"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
)

// blobExists reports whether a blob row is still present (refcount > 0
// rows are kept; a fully-released blob is DELETEd).
func blobExists(t *testing.T, s *Store, id int64) bool {
	t.Helper()
	var n int64
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM blobs WHERE id = ?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

// TestCloneShellCarriesScreenshot: a PTY can't be forked, so cloning a
// shell with a frozen preview must carry the preview blob (the screenshot)
// to the clone and bump that blob's refcount. Regression: CloneTile had no
// shell case, so the clone landed with preview_blob_id NULL and the blob
// refcount under-counted.
func TestCloneShellCarriesScreenshot(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	sh, err := s.CreateShell(ctx, &rpc.CreateShellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	framed, err := s.SetShellPreview(ctx, &rpc.SetShellPreviewRequest{
		TileID: sh.ID, JPEG: []byte("frozen-shell"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if framed.PreviewBlobID == 0 {
		t.Fatal("shell has no preview blob after SetShellPreview")
	}

	clone, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		TileID:     framed.ID,
		DestGridID: root, X: 50, Y: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if clone.Kind != rpc.KindShell {
		t.Errorf("clone kind = %q, want shell", clone.Kind)
	}
	if clone.PreviewBlobID != framed.PreviewBlobID {
		t.Errorf("clone preview blob = %d, want %d (screenshot must be carried)",
			clone.PreviewBlobID, framed.PreviewBlobID)
	}
	// Two tiles now reference the one preview blob.
	verifyRefcounts(t, s)
}

// TestCloneCopiesShellPreviewBlob: cloning a well whose child grid holds a
// shell-with-preview deep-copies the shell row, which references the same
// immutable preview blob, so its refcount must rise to 2. cloneSubtree must
// bump the preview blob for every copied tile that holds one.
func TestCloneCopiesShellPreviewBlob(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	well, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	sh, err := s.CreateShell(ctx, &rpc.CreateShellRequest{
		GridID: well.ChildGridID, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetShellPreview(ctx, &rpc.SetShellPreviewRequest{
		TileID: sh.ID, JPEG: []byte("frozen-shell"),
	}); err != nil {
		t.Fatal(err)
	}

	// Clone the well: copy-on-clone deep-copies its child grid, re-rowing the
	// shell-with-preview. The copy shares the immutable preview blob, so its
	// refcount must rise to 2 — the case cloneSubtree must get right.
	clone, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		TileID:     well.ID,
		DestGridID: root, X: 50, Y: 0,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Two shell rows (original + copy) now share one preview blob.
	cloneChild, err := s.GetGrid(ctx, clone.ChildGridID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cloneChild.Tiles) != 1 || cloneChild.Tiles[0].Kind != rpc.KindShell {
		t.Fatalf("clone child = %+v, want one shell tile", cloneChild.Tiles)
	}
	previewBlob := cloneChild.Tiles[0].PreviewBlobID
	if previewBlob == 0 {
		t.Fatal("cloned shell lost its preview blob")
	}
	if rc := refcount(t, s, "blobs", previewBlob); rc != 2 {
		t.Errorf("preview blob refcount = %d, want 2", rc)
	}
	verifyRefcounts(t, s)
}

// TestDeleteGridReleasesAllKindRefs: GC'ing a grid (its last well deleted)
// must release every reference its tiles held — in particular a shell's
// preview blob — so deleting the containing well doesn't leak the blob.
func TestDeleteGridReleasesAllKindRefs(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	well, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	sh, err := s.CreateShell(ctx, &rpc.CreateShellRequest{
		GridID: well.ChildGridID, X: 2, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	framed, err := s.SetShellPreview(ctx, &rpc.SetShellPreviewRequest{
		TileID: sh.ID, JPEG: []byte("frozen-shell"),
	})
	if err != nil {
		t.Fatal(err)
	}
	previewBlob := framed.PreviewBlobID
	if previewBlob == 0 {
		t.Fatalf("setup: previewBlob=%d", previewBlob)
	}

	// Destroy the only well pointing at the child grid (two-stage #262
	// gesture collapsed) → child grid GCs, cascading through the shell
	// inside it.
	hardDelete(t, s, well.ID)

	verifyRefcounts(t, s)
	if blobExists(t, s, previewBlob) {
		t.Errorf("shell preview blob %d survived GC — refcount leaked", previewBlob)
	}
}
