package fs_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/plugins/fs"
	"github.com/josephburnett/gridwell/plugins/fs/fssource"
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
	if _, err := p.PlaceTile(context.Background(), &gridwellv1.PlaceTileRequest{TileId: note.Id, X: 5, Y: 6, W: 1, H: 1}); err != nil {
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

// TestUnreadableDirKeepsRowsAndIdentity: a directory that EXISTS but cannot be
// read this pass (EACCES, an unmounted network share) is NOT an authoritative
// empty listing — the stored rows, their positions, AND their ids must survive
// untouched (invariant I12: a failed read must never sweep a tile; only a
// definite GONE does). Before the fix, GetGrid treated any read error as
// empty-authoritative and reconcileTiles deleted every row, so a transient
// hiccup re-rowed the grid with fresh ids at auto-layout positions.
// Injected via SetReadDir because these tests often run as root, where a
// chmod-based repro is impossible.
func TestUnreadableDirKeepsRowsAndIdentity(t *testing.T) {
	dir := tempTree(t) // note.txt + sub/
	p, _ := openFilePlugin(t)
	att, err := attachAt(p, dir)
	if err != nil {
		t.Fatal(err)
	}
	r, err := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: att.RootGridId})
	if err != nil {
		t.Fatal(err)
	}
	note := tileByName(t, r.Tiles, "note.txt")
	// The user arranges the tile; this placement is the state under test.
	if _, err := p.PlaceTile(context.Background(), &gridwellv1.PlaceTileRequest{TileId: note.Id, X: 5, Y: 6, W: 1, H: 1}); err != nil {
		t.Fatal(err)
	}

	// The directory becomes transiently unreadable (permission error — NOT
	// a does-not-exist).
	p.SetReadDir(func(string) ([]fssource.Entry, error) {
		return nil, fmt.Errorf("open %s: %w", dir, syscall.EACCES)
	})
	r2, err := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: att.RootGridId})
	if err != nil {
		t.Fatalf("GetGrid on an unreadable dir should not error, got %v", err)
	}
	note2 := tileByName(t, r2.Tiles, "note.txt")
	if note2.Id != note.Id {
		t.Errorf("tile id changed across an unreadable pass: %s -> %s", note.Id, note2.Id)
	}
	if note2.X != 5 || note2.Y != 6 {
		t.Errorf("placement lost across an unreadable pass: (%d,%d), want (5,6)", note2.X, note2.Y)
	}

	// The directory becomes readable again: same rows, same ids, same
	// placement — as if nothing happened.
	p.SetReadDir(nil)
	r3, err := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: att.RootGridId})
	if err != nil {
		t.Fatal(err)
	}
	note3 := tileByName(t, r3.Tiles, "note.txt")
	if note3.Id != note.Id {
		t.Errorf("tile id changed after readability returned: %s -> %s", note.Id, note3.Id)
	}
	if note3.X != 5 || note3.Y != 6 {
		t.Errorf("placement lost after readability returned: (%d,%d), want (5,6)", note3.X, note3.Y)
	}
}
