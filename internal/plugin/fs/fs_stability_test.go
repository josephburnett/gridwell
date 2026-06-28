package fs_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/plugin/fs"
)

// TestSubdirChildGridStableAcrossReopen: a directory's grid id is a stable
// handle. Descending into "sub/" yields a child grid id; after closing and
// reopening the same DB, the same subdir resolves to the SAME id — so a saved
// deep link into a directory keeps resolving. (the primary rule for fs: a
// path's placement/identity stays put as long as the path is present.)
func TestSubdirChildGridStableAcrossReopen(t *testing.T) {
	dir := tempTree(t) // has note.txt + sub/
	p, dbPath := openFilePlugin(t)
	att, err := attachAt(p, dir)
	if err != nil {
		t.Fatal(err)
	}
	r, err := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: att.RootGridId})
	if err != nil {
		t.Fatal(err)
	}
	sub := tileByName(t, r.Tiles, "sub")
	if sub.ChildGridId == "" {
		t.Fatal("subdir well has no child grid id")
	}
	subGrid := sub.ChildGridId
	p.Close()

	p2, err := fs.Open(dbPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p2.Close()
	att2, err := attachAt(p2, dir)
	if err != nil {
		t.Fatal(err)
	}
	if att2.RootGridId != att.RootGridId {
		t.Errorf("root grid id changed across reopen: %s -> %s", att.RootGridId, att2.RootGridId)
	}
	r2, err := p2.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: att2.RootGridId})
	if err != nil {
		t.Fatal(err)
	}
	sub2 := tileByName(t, r2.Tiles, "sub")
	if sub2.ChildGridId != subGrid {
		t.Errorf("subdir child grid id changed across reopen: %s -> %s", subGrid, sub2.ChildGridId)
	}
}

// TestGetGridUnreadablePathIsEmptyNotError: a directory that vanished (or can't
// be read) yields an empty authoritative grid, not an error — the file world is
// outside our control, so its disappearance is a normal empty listing, not a
// failure that would break navigation.
func TestGetGridUnreadablePathIsEmptyNotError(t *testing.T) {
	dir := t.TempDir()
	gone := filepath.Join(dir, "willvanish")
	if err := os.Mkdir(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	p, _ := openFilePlugin(t)
	att, err := attachAt(p, gone)
	if err != nil {
		t.Fatal(err)
	}
	// Remove the directory out from under the plugin.
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	r, err := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: att.RootGridId})
	if err != nil {
		t.Fatalf("GetGrid on a vanished dir should not error, got %v", err)
	}
	if len(r.Tiles) != 0 {
		t.Errorf("vanished dir listed %d tiles, want 0", len(r.Tiles))
	}
}

// TestNewFileAppearsKeepingExistingPlacement: a file added to the directory
// after the first listing shows up on the next GetGrid in a free cell, while
// the file the user had already moved keeps its position — the reconcile
// invariant that placement is persistent and only new arrivals auto-lay.
func TestNewFileAppearsKeepingExistingPlacement(t *testing.T) {
	dir := tempTree(t)
	p, _ := openFilePlugin(t)
	att, err := attachAt(p, dir)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: att.RootGridId})
	note := tileByName(t, r.Tiles, "note.txt")
	if _, err := p.MoveTile(context.Background(), &gridwellv1.MoveTileRequest{TileId: note.Id, X: 5, Y: 6}); err != nil {
		t.Fatal(err)
	}

	// A new file appears on disk.
	if err := os.WriteFile(filepath.Join(dir, "fresh.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	r2, err := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: att.RootGridId})
	if err != nil {
		t.Fatal(err)
	}
	note2 := tileByName(t, r2.Tiles, "note.txt")
	if note2.X != 5 || note2.Y != 6 {
		t.Errorf("moved note.txt drifted to (%d,%d), want (5,6)", note2.X, note2.Y)
	}
	fresh := tileByName(t, r2.Tiles, "fresh.txt")
	if fresh.X == 5 && fresh.Y == 6 {
		t.Error("new file landed on the moved tile's cell instead of a free one")
	}
}
