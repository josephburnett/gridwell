package store

import (
	"context"
	"testing"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// tileVersion reads a tile's current version straight from the row.
func tileVersion(t *testing.T, s *Store, tileID string) int64 {
	t.Helper()
	id, err := parseID(tileID)
	if err != nil {
		t.Fatal(err)
	}
	var v int64
	if err := s.db.QueryRow(`SELECT version FROM tiles WHERE id = ?`, id).Scan(&v); err != nil {
		t.Fatalf("read version: %v", err)
	}
	return v
}

// TestSetTextViewIsFramingNoVersionBump: a text tile's framed-window + mode is
// framing, not content. Persisting it must NOT bump the version — otherwise a
// reader that merely scrolls would invalidate every cached copy and clones
// would drift apart. (face #3 of the primary rule.)
func TestSetTextViewIsFramingNoVersionBump(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	tile, err := s.CreateText(ctx, &rpc.CreateTextRequest{GridID: root, X: 0, Y: 0, W: 2, H: 2, Data: []byte("# hi")})
	if err != nil {
		t.Fatal(err)
	}
	v0 := tile.Version

	out, err := s.SetTextView(ctx, &rpc.SetTextViewRequest{
		Path: rpc.Path{}, TileID: tile.ID, Version: v0,
		TextX: 10, TextY: 20, TextW: 300, TextH: 400, TextMode: "rendered",
	})
	if err != nil {
		t.Fatalf("SetTextView: %v", err)
	}
	if out.Version != v0 {
		t.Errorf("framing write bumped version %d -> %d", v0, out.Version)
	}
	if tileVersion(t, s, tile.ID) != v0 {
		t.Errorf("framing write bumped the stored version")
	}
	// The framing itself persisted.
	if out.TextW != 300 || out.TextMode != "rendered" {
		t.Errorf("framing not persisted: %+v", out)
	}
}

// TestSetShellPreviewIgnoresVersionButBumps pins the shell preview's unusual
// contract: it is content (the frozen frame), so it BUMPS the version — but
// unlike every other content edit it does NOT consult req.Version, because the
// shell's concurrency primitive is the live WebSocket session (one PTY per
// tile), not optimistic concurrency. So a deliberately stale version is
// accepted (no ErrVersionConflict) yet the write still advances the version.
func TestSetShellPreviewIgnoresVersionButBumps(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	tile, err := s.CreateShell(ctx, &rpc.CreateShellRequest{GridID: root, X: 0, Y: 0, W: 2, H: 2})
	if err != nil {
		t.Fatal(err)
	}
	v0 := tile.Version

	// A stale version (-1) would be a conflict for any normal mutation; the
	// shell preview accepts it.
	out, err := s.SetShellPreview(ctx, &rpc.SetShellPreviewRequest{
		Path: rpc.Path{}, TileID: tile.ID, Version: -1, JPEG: []byte("jpegbytes"),
	})
	if err != nil {
		t.Fatalf("SetShellPreview with stale version should be accepted, got %v", err)
	}
	if out.Version != v0+1 {
		t.Errorf("shell preview version = %d, want %d (content write bumps)", out.Version, v0+1)
	}
}

// TestSetTileAltBumpsVersion: alt_text IS content (it changes the markdown a
// drop produces), so SetTileAlt bumps the version — the contrast that proves
// the framing-vs-content split is real, not incidental.
func TestSetTileAltBumpsVersion(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	tile, err := s.CreateText(ctx, &rpc.CreateTextRequest{GridID: root, X: 0, Y: 0, W: 2, H: 2, Data: []byte("# hi")})
	if err != nil {
		t.Fatal(err)
	}
	v0 := tile.Version
	id, _ := parseID(tile.ID)

	if err := s.SetTileAlt(ctx, id, "a new label", false); err != nil {
		t.Fatalf("SetTileAlt: %v", err)
	}
	if got := tileVersion(t, s, tile.ID); got != v0+1 {
		t.Errorf("SetTileAlt version = %d, want %d (content edit must bump)", got, v0+1)
	}
}
