package store

import (
	"context"
	"testing"

	"github.com/josephburnett/gridwell/internal/rpc"
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

func gridExists(t *testing.T, s *Store, id int64) bool {
	t.Helper()
	var n int64
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM grids WHERE id = ?`, id).Scan(&n); err != nil {
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
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	framed, err := s.SetShellPreview(ctx, &rpc.SetShellPreviewRequest{
		Path: rpc.Path{}, TileID: sh.ID, Version: sh.Version, JPEG: []byte("frozen-shell"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if framed.PreviewBlobID == 0 {
		t.Fatal("shell has no preview blob after SetShellPreview")
	}

	clone, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		Path: rpc.Path{}, TileID: framed.ID, Version: framed.Version,
		DestGridID: root, DestPath: rpc.Path{}, X: 50, Y: 0,
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

// TestForkPreservesShellPreviewRefcount: when a shared grid containing a
// shell-with-preview is forked (COW), the copied shell row references the
// same preview blob, so its refcount must rise to 2. Regression: forkGrid
// had no shell case and copied the row without bumping the blob.
func TestForkPreservesShellPreviewRefcount(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	well, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	inner := rpc.Path{WellIDs: []int64{well.ID}}
	sh, err := s.CreateShell(ctx, &rpc.CreateShellRequest{
		Path: inner, GridID: well.ChildGridID, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetShellPreview(ctx, &rpc.SetShellPreviewRequest{
		Path: inner, TileID: sh.ID, Version: sh.Version, JPEG: []byte("frozen-shell"),
	}); err != nil {
		t.Fatal(err)
	}

	// Clone the well: now its child grid is shared (refcount 2).
	clone, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
		Path: rpc.Path{}, TileID: well.ID, Version: well.Version,
		DestGridID: root, DestPath: rpc.Path{}, X: 50, Y: 0,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Mutate through the clone's path: this forks the shared child grid,
	// copying the shell-with-preview into the fork.
	if _, err := s.CreateText(ctx, &rpc.CreateTextRequest{
		Path:   rpc.Path{WellIDs: []int64{clone.ID}},
		GridID: clone.ChildGridID, X: 5, Y: 5, W: 1, H: 1, Data: []byte("forces a fork"),
	}); err != nil {
		t.Fatal(err)
	}

	// Two shell rows (original + forked copy) now share one preview blob.
	verifyRefcounts(t, s)
}

// TestDeleteGridReleasesAllKindRefs: GC'ing a grid (its last well deleted)
// must release every reference its tiles held — the file-well's backing
// fs-grid and the shell's preview blob. Regression: deleteGrid only
// decremented plain wells and text/url blobs, leaking the fs/proc grid
// refcount and the shell preview blob.
func TestDeleteGridReleasesAllKindRefs(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	well, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	inner := rpc.Path{WellIDs: []int64{well.ID}}

	fw, err := s.CreateFileWell(ctx, &rpc.CreateFileWellRequest{
		Path: inner, GridID: well.ChildGridID, X: 0, Y: 0, W: 1, H: 1, FSPath: "/etc",
	})
	if err != nil {
		t.Fatal(err)
	}
	sh, err := s.CreateShell(ctx, &rpc.CreateShellRequest{
		Path: inner, GridID: well.ChildGridID, X: 2, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	framed, err := s.SetShellPreview(ctx, &rpc.SetShellPreviewRequest{
		Path: inner, TileID: sh.ID, Version: sh.Version, JPEG: []byte("frozen-shell"),
	})
	if err != nil {
		t.Fatal(err)
	}
	fsGrid := fw.ChildGridID
	previewBlob := framed.PreviewBlobID
	if fsGrid == 0 || previewBlob == 0 {
		t.Fatalf("setup: fsGrid=%d previewBlob=%d", fsGrid, previewBlob)
	}

	// Delete the only well pointing at the child grid → child grid GCs,
	// cascading through the file-well and shell inside it.
	if err := s.DeleteTile(ctx, &rpc.DeleteTileRequest{
		Path: rpc.Path{}, TileID: well.ID, Version: well.Version,
	}); err != nil {
		t.Fatal(err)
	}

	verifyRefcounts(t, s)
	if gridExists(t, s, fsGrid) {
		t.Errorf("fs-grid %d survived GC — file-well refcount leaked", fsGrid)
	}
	if blobExists(t, s, previewBlob) {
		t.Errorf("shell preview blob %d survived GC — refcount leaked", previewBlob)
	}
}
