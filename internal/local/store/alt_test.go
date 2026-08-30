package store

import (
	"context"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
)

// Issue #61: a USER-set name (the rename gesture) owns alt_text — the
// automatic captures (a url's page title on freeze, a shell's foreground
// command on detach) must never overwrite it. The latch is the alt_user
// column; SetTileAlt's user flag is its only writer.

func TestUserRenameWinsOverCaptures(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)

	tile, err := s.CreateURL(ctx, &rpc.CreateURLRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1, URL: "https://example.com",
	})
	if err != nil {
		t.Fatalf("CreateURL: %v", err)
	}
	id := mustParseID(t, tile.ID)

	// A capture before any rename lands normally.
	if err := s.SetTileAlt(ctx, id, "captured-title", false); err != nil {
		t.Fatalf("capture: %v", err)
	}
	if got := altOf(t, s, tile.ID); got != "captured-title" {
		t.Fatalf("alt = %q, want the capture", got)
	}

	// The user renames: this owns the name from now on.
	if err := s.SetTileAlt(ctx, id, "my-name", true); err != nil {
		t.Fatalf("user rename: %v", err)
	}

	// A later capture must NOT overwrite (and must not bump the version).
	before, _ := s.GetTile(ctx, tile.ID)
	if err := s.SetTileAlt(ctx, id, "sneaky-capture", false); err != nil {
		t.Fatalf("post-rename capture errored (should no-op): %v", err)
	}
	after, _ := s.GetTile(ctx, tile.ID)
	if got := altOf(t, s, tile.ID); got != "my-name" {
		t.Errorf("alt = %q, want the user's name to survive the capture", got)
	}
	if after.Version != before.Version {
		t.Errorf("a skipped capture bumped the version %d -> %d", before.Version, after.Version)
	}

	// The url-title capture path (SetURLState) must respect the latch too.
	if _, err := s.SetURLState(ctx, &rpc.SetURLStateRequest{
		TileID: tile.ID, URL: "https://example.com/x", Title: "page-title",
	}); err != nil {
		t.Fatalf("SetURLState: %v", err)
	}
	if got := altOf(t, s, tile.ID); got != "my-name" {
		t.Errorf("alt = %q after SetURLState, want the user's name", got)
	}

	// A SECOND user rename does overwrite.
	if err := s.SetTileAlt(ctx, id, "renamed-again", true); err != nil {
		t.Fatalf("second rename: %v", err)
	}
	if got := altOf(t, s, tile.ID); got != "renamed-again" {
		t.Errorf("alt = %q, want the second rename", got)
	}
}

// TestURLTitleCaptureStillWorksUnnamed: without a user rename, the title
// capture keeps stamping alt (the pre-#61 behavior stays for unnamed tiles).
func TestURLTitleCaptureStillWorksUnnamed(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)
	tile, err := s.CreateURL(ctx, &rpc.CreateURLRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1, URL: "https://example.com",
	})
	if err != nil {
		t.Fatalf("CreateURL: %v", err)
	}
	if _, err := s.SetURLState(ctx, &rpc.SetURLStateRequest{
		TileID: tile.ID, Title: "page-title",
	}); err != nil {
		t.Fatalf("SetURLState: %v", err)
	}
	if got := altOf(t, s, tile.ID); got != "page-title" {
		t.Errorf("alt = %q, want the captured title", got)
	}
}

func altOf(t *testing.T, s *Store, tileID string) string {
	t.Helper()
	tile, err := s.GetTile(context.Background(), tileID)
	if err != nil {
		t.Fatalf("GetTile: %v", err)
	}
	return tile.AltText
}

func mustParseID(t *testing.T, id string) int64 {
	t.Helper()
	n, err := parseID(id)
	if err != nil {
		t.Fatalf("parseID(%q): %v", id, err)
	}
	return n
}
