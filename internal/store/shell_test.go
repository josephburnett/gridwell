package store

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// TestCreateShellStampsHomeAsCwd: a brand-new shell tile's shell_cwd
// must be non-empty before the first refresh runs, so the bash session
// has somewhere to start. The server stamps $HOME (or falls back to
// the server's own cwd).
func TestCreateShellStampsHomeAsCwd(t *testing.T) {
	t.Setenv("HOME", "/home/test-user")
	s := newTestStore(t)
	root := rootID(t, s)
	tile, err := s.CreateShell(context.Background(), &rpc.CreateShellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if tile.Kind != rpc.KindShell {
		t.Errorf("kind = %q, want shell", tile.Kind)
	}
	if tile.ShellCwd != "/home/test-user" {
		t.Errorf("ShellCwd = %q, want /home/test-user", tile.ShellCwd)
	}
	if tile.AltText != "shell" {
		t.Errorf("AltText = %q, want shell", tile.AltText)
	}
	if tile.PreviewBlobID != 0 {
		t.Errorf("PreviewBlobID = %d, want 0 (no JPEG yet)", tile.PreviewBlobID)
	}
}

// TestCreateShellFallsBackToCwdWhenHomeUnset: if $HOME isn't set the
// server uses its own working dir rather than failing. Guards against
// sandboxed setups (systemd unit without HOME) that would otherwise
// leave shell tiles uncreatable.
func TestCreateShellFallsBackToCwdWhenHomeUnset(t *testing.T) {
	t.Setenv("HOME", "")
	s := newTestStore(t)
	root := rootID(t, s)
	tile, err := s.CreateShell(context.Background(), &rpc.CreateShellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if tile.ShellCwd == "" {
		t.Errorf("ShellCwd empty with HOME unset; want server cwd fallback")
	}
	if _, err := os.Stat(tile.ShellCwd); err != nil {
		t.Errorf("ShellCwd %q does not exist: %v", tile.ShellCwd, err)
	}
}

// TestSetShellCwdPersists: the freeze path persists wherever bash had
// 'cd'-ed to. The next refresh reads this and resumes there.
func TestSetShellCwdPersists(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	tile, err := s.CreateShell(ctx, &rpc.CreateShellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := s.SetShellCwd(ctx, &rpc.SetShellCwdRequest{
		TileID: tile.ID, Version: tile.Version, ShellCwd: "/tmp/work",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ShellCwd != "/tmp/work" {
		t.Errorf("ShellCwd = %q, want /tmp/work", updated.ShellCwd)
	}
	if updated.Version != tile.Version+1 {
		t.Errorf("version did not bump: %d -> %d", tile.Version, updated.Version)
	}
}

// TestSetShellCwdRejectsNonShell: defensive — only shell tiles carry
// shell_cwd. A misbehaving client trying to set it on a text tile must
// be rejected at the boundary.
func TestSetShellCwdRejectsNonShell(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	text, err := s.CreateText(ctx, &rpc.CreateTextRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1, Data: []byte("x"),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.SetShellCwd(ctx, &rpc.SetShellCwdRequest{
		TileID: text.ID, Version: text.Version, ShellCwd: "/tmp",
	})
	if !errors.Is(err, ErrNotShellTile) {
		t.Errorf("got %v, want ErrNotShellTile", err)
	}
}

// TestSetShellPreviewStoresAndDedupes: setting the JPEG hashes the
// bytes through the blob table, and an identical second write does not
// create a new blob row.
func TestSetShellPreviewStoresAndDedupes(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	tile, err := s.CreateShell(ctx, &rpc.CreateShellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	jpeg := []byte("fake-jpeg-bytes")
	v1, err := s.SetShellPreview(ctx, &rpc.SetShellPreviewRequest{
		TileID: tile.ID, Version: tile.Version, JPEG: jpeg,
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
		TileID: v1.ID, Version: v1.Version, JPEG: jpeg,
	})
	if err != nil {
		t.Fatal(err)
	}
	if v2.PreviewBlobID != v1.PreviewBlobID {
		t.Errorf("PreviewBlobID changed across identical writes: %d -> %d", v1.PreviewBlobID, v2.PreviewBlobID)
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
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	v1, err := s.SetShellPreview(ctx, &rpc.SetShellPreviewRequest{
		TileID: tile.ID, Version: tile.Version, JPEG: []byte("abc"),
	})
	if err != nil {
		t.Fatal(err)
	}
	v2, err := s.SetShellPreview(ctx, &rpc.SetShellPreviewRequest{
		TileID: v1.ID, Version: v1.Version, JPEG: nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	if v2.PreviewBlobID != 0 {
		t.Errorf("PreviewBlobID after clear = %d, want 0", v2.PreviewBlobID)
	}
}

// TestUpdateTextRejectsShell: the read-only contract for text-like
// tiles extends to shell — UpdateText on a shell tile must fail at the
// kind check.
func TestUpdateTextRejectsShell(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	tile, err := s.CreateShell(ctx, &rpc.CreateShellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.UpdateText(ctx, &rpc.UpdateTextRequest{
		TileID: tile.ID, Version: tile.Version, Data: []byte("nope"),
	})
	if !errors.Is(err, ErrNotTextTile) {
		t.Errorf("got %v, want ErrNotTextTile", err)
	}
}
