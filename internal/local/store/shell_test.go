package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
)

// TestCreateShellCreatesFrozenTile: a brand-new shell tile must
// land with kind=shell, the default alt_text, and no preview yet.
// With the tmux backing the cwd is no longer tile state — bash's
// directory lives inside tmux from the first refresh onward — so
// this just covers the initial-row invariants.
func TestCreateShellCreatesFrozenTile(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	tile, err := s.CreateShell(context.Background(), &rpc.CreateShellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if tile.Kind != rpc.KindShell {
		t.Errorf("kind = %q, want shell", tile.Kind)
	}
	if tile.AltText != "shell" {
		t.Errorf("AltText = %q, want shell", tile.AltText)
	}
	if tile.PreviewBlobID != 0 {
		t.Errorf("PreviewBlobID = %d, want 0 (no JPEG yet)", tile.PreviewBlobID)
	}
}

// TestSetShellPreviewStoresAndDedupes: setting the JPEG hashes the
// bytes through the blob table, and an identical second write does
// not create a new blob row.
func TestSetShellPreviewStoresAndDedupes(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	tile, err := s.CreateShell(ctx, &rpc.CreateShellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	jpeg := []byte("fake-jpeg-bytes")
	v1, err := s.SetShellPreview(ctx, &rpc.SetShellPreviewRequest{
		TileID: tile.ID, JPEG: jpeg,
	})
	if err != nil {
		t.Fatal(err)
	}
	if v1.PreviewBlobID == 0 {
		t.Fatalf("PreviewBlobID still 0 after SetShellPreview")
	}
	got, err := s.GetTilePreview(ctx, tile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(jpeg) {
		t.Errorf("preview bytes = %q, want %q", got, jpeg)
	}

	// Identical second write — blob row should dedupe.
	v2, err := s.SetShellPreview(ctx, &rpc.SetShellPreviewRequest{
		TileID: v1.ID, JPEG: jpeg,
	})
	if err != nil {
		t.Fatal(err)
	}
	if v2.PreviewBlobID != v1.PreviewBlobID {
		t.Errorf("PreviewBlobID changed across identical writes: %d -> %d", v1.PreviewBlobID, v2.PreviewBlobID)
	}
}

// TestSetShellPreviewOverwritesFrozenFrame: the frozen frame is a capture,
// so the freeze path can race other mutations (a concurrent SetShellPreview
// from a takeover handler, the detach-time title capture) and must simply
// land — last writer wins, no claim to lose. (version_rule_test.go pins the
// no-claim/no-bump half; this pins that a second capture replaces the
// first.)
func TestSetShellPreviewOverwritesFrozenFrame(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	tile, err := s.CreateShell(ctx, &rpc.CreateShellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	// A first capture, then a second one over it.
	if _, err := s.SetShellPreview(ctx, &rpc.SetShellPreviewRequest{
		TileID: tile.ID, JPEG: []byte("first"),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.SetShellPreview(ctx, &rpc.SetShellPreviewRequest{
		TileID: tile.ID, JPEG: []byte("frozen"),
	})
	if err != nil {
		t.Fatalf("second SetShellPreview: %v", err)
	}
	if got.PreviewBlobID == 0 {
		t.Errorf("preview did not land")
	}
}

// TestSetShellPreviewClearsOnEmpty: passing empty bytes clears the
// preview pointer and drops the old blob's refcount — useful as a
// reset after a failed refresh.
func TestSetShellPreviewClearsOnEmpty(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	tile, err := s.CreateShell(ctx, &rpc.CreateShellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	v1, err := s.SetShellPreview(ctx, &rpc.SetShellPreviewRequest{
		TileID: tile.ID, JPEG: []byte("abc"),
	})
	if err != nil {
		t.Fatal(err)
	}
	v2, err := s.SetShellPreview(ctx, &rpc.SetShellPreviewRequest{
		TileID: v1.ID, JPEG: nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	if v2.PreviewBlobID != 0 {
		t.Errorf("PreviewBlobID after clear = %d, want 0", v2.PreviewBlobID)
	}
}

// TestDeleteShellDropsPreviewBlob: deleting a shell tile must drop
// the refcount on its preview blob the same way URL tiles do, so
// deleting a tile that had a preview leaks zero bytes.
func TestDeleteShellDropsPreviewBlob(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	tile, err := s.CreateShell(ctx, &rpc.CreateShellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	stamped, err := s.SetShellPreview(ctx, &rpc.SetShellPreviewRequest{
		TileID: tile.ID, JPEG: []byte("frozen-frame"),
	})
	if err != nil {
		t.Fatal(err)
	}
	blobID := stamped.PreviewBlobID
	if blobID == 0 {
		t.Fatal("preview blob never stored; setup broken")
	}
	rc, err := blobRefcount(ctx, s, blobID)
	if err != nil {
		t.Fatal(err)
	}
	if rc != 1 {
		t.Errorf("refcount after SetShellPreview = %d, want 1", rc)
	}
	hardDelete(t, s, stamped.ID)
	rc, err = blobRefcount(ctx, s, blobID)
	if errors.Is(err, errBlobGone) {
		return // blob row collected on rc=0, fine
	}
	if err != nil {
		t.Fatal(err)
	}
	if rc != 0 {
		t.Errorf("refcount after DeleteTile = %d, want 0 (or row gone)", rc)
	}
}

// blobRefcount reads the refcount column for blobID. Returns errBlobGone
// if the row was reaped by the refcount-zero collector.
var errBlobGone = errors.New("blob deleted")

func blobRefcount(ctx context.Context, s *Store, blobID int64) (int64, error) {
	var rc int64
	err := s.db.QueryRowContext(ctx, `SELECT refcount FROM blobs WHERE id = ?`, blobID).Scan(&rc)
	if err == sql.ErrNoRows {
		return 0, errBlobGone
	}
	return rc, err
}

// TestUpdateTextRejectsShell: the read-only contract for text-like
// tiles extends to shell — UpdateText on a shell tile must fail at
// the kind check.
func TestUpdateTextRejectsShell(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	tile, err := s.CreateShell(ctx, &rpc.CreateShellRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.WriteContent(ctx, tile.ID, tile.Version, []byte("nope"))
	if !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("got %v, want ErrInvalidArgument (shell content is the frozen preview)", err)
	}
}
