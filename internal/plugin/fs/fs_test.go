package fs_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/plugin/fs"
)

// recordHost records remove calls without touching disk.
type recordHost struct {
	removed    []string
	removedAll []string
}

func (h *recordHost) Remove(p string) error    { h.removed = append(h.removed, p); return nil }
func (h *recordHost) RemoveAll(p string) error { h.removedAll = append(h.removedAll, p); return nil }

// tempTree creates a directory with "note.txt" and "sub/" subdir.
func tempTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func openPlugin(t *testing.T) *fs.Plugin {
	t.Helper()
	p, err := fs.Open(":memory:", nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func openPluginWithHost(t *testing.T, h fs.Host) *fs.Plugin {
	t.Helper()
	p, err := fs.Open(":memory:", h)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func TestOpen_InMemory(t *testing.T) {
	p, err := fs.Open(":memory:", nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	p.Close()
}

func TestInfo(t *testing.T) {
	p := openPlugin(t)
	resp, err := p.Info(context.Background(), &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if resp.Kind != "fs" {
		t.Errorf("Kind: got %q, want %q", resp.Kind, "fs")
	}
}

func TestAttach_ValidPath(t *testing.T) {
	p := openPlugin(t)
	resp, err := p.Attach(context.Background(), &gridwellv1.AttachRequest{
		Config: map[string]string{"path": "/home/joe"},
	})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if resp.RootGridId == "" {
		t.Errorf("RootGridId: got %q, want non-empty", resp.RootGridId)
	}
	if resp.Label != "joe" {
		t.Errorf("Label: got %q, want %q", resp.Label, "joe")
	}
}

func TestAttach_RootPath(t *testing.T) {
	p := openPlugin(t)
	resp, err := p.Attach(context.Background(), &gridwellv1.AttachRequest{
		Config: map[string]string{"path": "/"},
	})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if resp.Label != "files" {
		t.Errorf("Label: got %q, want %q", resp.Label, "files")
	}
}

func TestAttach_MissingPath(t *testing.T) {
	p := openPlugin(t)
	_, err := p.Attach(context.Background(), &gridwellv1.AttachRequest{
		Config: map[string]string{},
	})
	if err == nil {
		t.Error("expected error for empty path")
	}
}

func TestGetGrid_ListsEntries(t *testing.T) {
	dir := tempTree(t)
	p := openPlugin(t)

	att, err := p.Attach(context.Background(), &gridwellv1.AttachRequest{
		Config: map[string]string{"path": dir},
	})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	resp, err := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: att.RootGridId})
	if err != nil {
		t.Fatalf("GetGrid: %v", err)
	}
	if len(resp.Tiles) != 2 {
		t.Fatalf("got %d tiles, want 2: %+v", len(resp.Tiles), resp.Tiles)
	}

	byName := map[string]*gridwellv1.Tile{}
	for _, t2 := range resp.Tiles {
		byName[t2.AltText] = t2
	}

	noteTile, ok := byName["note.txt"]
	if !ok {
		t.Fatal("note.txt tile missing")
	}
	if noteTile.Kind != "text" {
		t.Errorf("note.txt kind: got %q, want text", noteTile.Kind)
	}

	subTile, ok := byName["sub"]
	if !ok {
		t.Fatal("sub tile missing")
	}
	if subTile.Kind != "well" {
		t.Errorf("sub kind: got %q, want well", subTile.Kind)
	}
	if subTile.ChildGridId == "" || subTile.ChildGridId == "0" {
		t.Error("sub tile should have child_grid_id != 0")
	}
}

func TestGetGrid_AutoLayout(t *testing.T) {
	dir := t.TempDir()
	// Create 3 files: a.txt, b.txt, c.txt
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644)
	}

	p := openPlugin(t)
	att, _ := p.Attach(context.Background(), &gridwellv1.AttachRequest{
		Config: map[string]string{"path": dir},
	})
	resp, err := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: att.RootGridId})
	if err != nil {
		t.Fatalf("GetGrid: %v", err)
	}
	if len(resp.Tiles) != 3 {
		t.Fatalf("got %d tiles, want 3", len(resp.Tiles))
	}
	// All tiles should be at distinct (x,y) positions.
	positions := map[[2]int64]bool{}
	for _, tile := range resp.Tiles {
		pos := [2]int64{tile.X, tile.Y}
		if positions[pos] {
			t.Errorf("duplicate position (%d,%d)", tile.X, tile.Y)
		}
		positions[pos] = true
	}
}

func TestGetGrid_StableIDs(t *testing.T) {
	dir := tempTree(t)
	p := openPlugin(t)
	att, _ := p.Attach(context.Background(), &gridwellv1.AttachRequest{
		Config: map[string]string{"path": dir},
	})
	gridID := att.RootGridId

	r1, err := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: gridID})
	if err != nil {
		t.Fatalf("GetGrid 1: %v", err)
	}
	r2, err := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: gridID})
	if err != nil {
		t.Fatalf("GetGrid 2: %v", err)
	}

	ids1 := map[string]string{}
	for _, tile := range r1.Tiles {
		ids1[tile.AltText] = tile.Id
	}
	for _, tile := range r2.Tiles {
		if ids1[tile.AltText] != tile.Id {
			t.Errorf("tile %q: id changed %s→%s", tile.AltText, ids1[tile.AltText], tile.Id)
		}
	}
}

func TestGetGrid_NewFileAppearsStably(t *testing.T) {
	dir := tempTree(t)
	p := openPlugin(t)
	att, _ := p.Attach(context.Background(), &gridwellv1.AttachRequest{
		Config: map[string]string{"path": dir},
	})
	gridID := att.RootGridId

	r1, _ := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: gridID})
	ids1 := map[string]string{}
	for _, tile := range r1.Tiles {
		ids1[tile.AltText] = tile.Id
	}

	// Add a new file.
	os.WriteFile(filepath.Join(dir, "new.md"), []byte("new"), 0o644)

	r2, _ := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: gridID})
	if len(r2.Tiles) != 3 {
		t.Fatalf("after add: got %d tiles, want 3", len(r2.Tiles))
	}
	for _, tile := range r2.Tiles {
		if tile.AltText == "new.md" {
			continue // new tile, no prior id
		}
		if ids1[tile.AltText] != tile.Id {
			t.Errorf("existing tile %q id changed: %s→%s", tile.AltText, ids1[tile.AltText], tile.Id)
		}
	}
}

func TestGetGrid_RemovesTilesForDeletedFiles(t *testing.T) {
	dir := tempTree(t)
	p := openPlugin(t)
	att, _ := p.Attach(context.Background(), &gridwellv1.AttachRequest{
		Config: map[string]string{"path": dir},
	})
	gridID := att.RootGridId

	p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: gridID})

	// Delete file from disk.
	os.Remove(filepath.Join(dir, "note.txt"))

	r2, _ := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: gridID})
	for _, tile := range r2.Tiles {
		if tile.AltText == "note.txt" {
			t.Error("note.txt tile should be removed after file deleted")
		}
	}
	if len(r2.Tiles) != 1 {
		t.Errorf("got %d tiles after delete, want 1", len(r2.Tiles))
	}
}

func TestGetGrid_MissingDir_ReturnsEmptyNotError(t *testing.T) {
	p := openPlugin(t)
	att, err := p.Attach(context.Background(), &gridwellv1.AttachRequest{
		Config: map[string]string{"path": "/nonexistent/path/that/doesnt/exist"},
	})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	resp, err := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: att.RootGridId})
	if err != nil {
		t.Fatalf("GetGrid missing dir: %v", err)
	}
	if len(resp.Tiles) != 0 {
		t.Errorf("got %d tiles for missing dir, want 0", len(resp.Tiles))
	}
}

func TestProbe_Present(t *testing.T) {
	dir := tempTree(t)
	p := openPlugin(t)
	att, _ := p.Attach(context.Background(), &gridwellv1.AttachRequest{
		Config: map[string]string{"path": dir},
	})
	resp, _ := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: att.RootGridId})

	var noteID string
	for _, tile := range resp.Tiles {
		if tile.AltText == "note.txt" {
			noteID = tile.Id
		}
	}
	if noteID == "" {
		t.Fatal("note.txt tile not found")
	}

	probeResp, err := p.Probe(context.Background(), &gridwellv1.ProbeRequest{TileId: noteID})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if probeResp.Presence != gridwellv1.ProbeResponse_PRESENCE_PRESENT {
		t.Errorf("Presence: got %v, want PRESENT", probeResp.Presence)
	}
}

func TestProbe_Gone(t *testing.T) {
	p := openPlugin(t)
	// Non-existent tile_id.
	probeResp, err := p.Probe(context.Background(), &gridwellv1.ProbeRequest{TileId: "99999"})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if probeResp.Presence != gridwellv1.ProbeResponse_PRESENCE_GONE {
		t.Errorf("Presence: got %v, want GONE", probeResp.Presence)
	}
}

func TestDeleteTile_File(t *testing.T) {
	// Use a real host (nil) so the file is actually removed from disk,
	// which lets the subsequent GetGrid see it gone via reconcile.
	dir := tempTree(t)
	p := openPlugin(t)
	att, _ := p.Attach(context.Background(), &gridwellv1.AttachRequest{
		Config: map[string]string{"path": dir},
	})
	resp, _ := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: att.RootGridId})

	var noteID string
	for _, tile := range resp.Tiles {
		if tile.AltText == "note.txt" {
			noteID = tile.Id
		}
	}
	if noteID == "" {
		t.Fatal("note.txt tile not found")
	}

	_, err := p.DeleteTile(context.Background(), &gridwellv1.DeleteTileRequest{TileId: noteID})
	if err != nil {
		t.Fatalf("DeleteTile: %v", err)
	}

	// File should be removed from disk and from GetGrid.
	if _, statErr := os.Lstat(filepath.Join(dir, "note.txt")); statErr == nil {
		t.Error("note.txt should have been removed from disk")
	}
	resp2, _ := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: att.RootGridId})
	for _, tile := range resp2.Tiles {
		if tile.AltText == "note.txt" {
			t.Error("note.txt still appears after DeleteTile")
		}
	}
}

func TestDeleteTile_CallsRemoveMethod(t *testing.T) {
	// Verify the Remove method is called with the correct path.
	dir := tempTree(t)
	h := &recordHost{}
	p := openPluginWithHost(t, h)
	att, _ := p.Attach(context.Background(), &gridwellv1.AttachRequest{
		Config: map[string]string{"path": dir},
	})
	resp, _ := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: att.RootGridId})

	var noteID string
	for _, tile := range resp.Tiles {
		if tile.AltText == "note.txt" {
			noteID = tile.Id
		}
	}
	if noteID == "" {
		t.Fatal("note.txt tile not found")
	}

	_, err := p.DeleteTile(context.Background(), &gridwellv1.DeleteTileRequest{TileId: noteID})
	if err != nil {
		t.Fatalf("DeleteTile: %v", err)
	}
	if len(h.removed) != 1 || h.removed[0] != filepath.Join(dir, "note.txt") {
		t.Errorf("removed = %v, want [%s]", h.removed, filepath.Join(dir, "note.txt"))
	}
}

func TestDeleteTile_Dir(t *testing.T) {
	dir := tempTree(t)
	h := &recordHost{}
	p := openPluginWithHost(t, h)
	att, _ := p.Attach(context.Background(), &gridwellv1.AttachRequest{
		Config: map[string]string{"path": dir},
	})
	resp, _ := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: att.RootGridId})

	var subID string
	for _, tile := range resp.Tiles {
		if tile.AltText == "sub" {
			subID = tile.Id
		}
	}
	if subID == "" {
		t.Fatal("sub tile not found")
	}

	_, err := p.DeleteTile(context.Background(), &gridwellv1.DeleteTileRequest{TileId: subID})
	if err != nil {
		t.Fatalf("DeleteTile dir: %v", err)
	}
	if len(h.removedAll) != 1 {
		t.Errorf("expected 1 RemoveAll call, got %d", len(h.removedAll))
	}
}

func TestDeleteTile_MissingIsOK(t *testing.T) {
	p := openPlugin(t)
	_, err := p.DeleteTile(context.Background(), &gridwellv1.DeleteTileRequest{TileId: "99999"})
	if err != nil {
		t.Fatalf("DeleteTile missing: %v", err)
	}
}

// openFilePlugin opens a plugin backed by a real file (not :memory:) so a
// reopen test can verify positions survive a process restart. Returns the
// plugin and the db path.
func openFilePlugin(t *testing.T) (*fs.Plugin, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "fs.db")
	p, err := fs.Open(dbPath, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	return p, dbPath
}

// tileByName returns the tile with the given AltText (directory entry name).
func tileByName(t *testing.T, tiles []*gridwellv1.Tile, name string) *gridwellv1.Tile {
	t.Helper()
	for _, tile := range tiles {
		if tile.AltText == name {
			return tile
		}
	}
	t.Fatalf("tile %q not found", name)
	return nil
}

// TestMoveTile_Persists: a moved tile keeps its new position across a
// subsequent GetGrid (reconcile must not re-lay-out an existing entry). This
// is the "placement is persistent" face of the primary rule.
func TestMoveTile_Persists(t *testing.T) {
	dir := tempTree(t)
	p := openPlugin(t)
	att, _ := p.Attach(context.Background(), &gridwellv1.AttachRequest{Config: map[string]string{"path": dir}})
	r, _ := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: att.RootGridId})
	note := tileByName(t, r.Tiles, "note.txt")

	moved, err := p.MoveTile(context.Background(), &gridwellv1.MoveTileRequest{TileId: note.Id, X: 5, Y: 7})
	if err != nil {
		t.Fatalf("MoveTile: %v", err)
	}
	if moved.Tile.X != 5 || moved.Tile.Y != 7 {
		t.Fatalf("MoveTile returned (%d,%d), want (5,7)", moved.Tile.X, moved.Tile.Y)
	}

	r2, _ := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: att.RootGridId})
	note2 := tileByName(t, r2.Tiles, "note.txt")
	if note2.X != 5 || note2.Y != 7 {
		t.Errorf("after GetGrid note.txt at (%d,%d), want (5,7)", note2.X, note2.Y)
	}
	if note2.Id != note.Id {
		t.Errorf("note.txt id changed %s→%s (must never re-row)", note.Id, note2.Id)
	}
}

// TestMoveTile_SurvivesReopen: a moved tile keeps its position after the
// plugin DB is closed and reopened — placement persists across a restart.
func TestMoveTile_SurvivesReopen(t *testing.T) {
	dir := tempTree(t)
	p, dbPath := openFilePlugin(t)
	att, _ := p.Attach(context.Background(), &gridwellv1.AttachRequest{Config: map[string]string{"path": dir}})
	gridID := att.RootGridId
	r, _ := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: gridID})
	note := tileByName(t, r.Tiles, "note.txt")
	if _, err := p.MoveTile(context.Background(), &gridwellv1.MoveTileRequest{TileId: note.Id, X: 3, Y: 4}); err != nil {
		t.Fatalf("MoveTile: %v", err)
	}
	p.Close()

	p2, err := fs.Open(dbPath, nil)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer p2.Close()
	r2, err := p2.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: gridID})
	if err != nil {
		t.Fatalf("GetGrid after reopen: %v", err)
	}
	note2 := tileByName(t, r2.Tiles, "note.txt")
	if note2.X != 3 || note2.Y != 4 {
		t.Errorf("after reopen note.txt at (%d,%d), want (3,4)", note2.X, note2.Y)
	}
}

// TestResizeTile_Persists: a resized tile keeps its footprint across GetGrid.
func TestResizeTile_Persists(t *testing.T) {
	dir := tempTree(t)
	p := openPlugin(t)
	att, _ := p.Attach(context.Background(), &gridwellv1.AttachRequest{Config: map[string]string{"path": dir}})
	r, _ := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: att.RootGridId})
	sub := tileByName(t, r.Tiles, "sub")

	if _, err := p.ResizeTile(context.Background(), &gridwellv1.ResizeTileRequest{TileId: sub.Id, X: 2, Y: 2, W: 3, H: 4}); err != nil {
		t.Fatalf("ResizeTile: %v", err)
	}
	r2, _ := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: att.RootGridId})
	sub2 := tileByName(t, r2.Tiles, "sub")
	if sub2.X != 2 || sub2.Y != 2 || sub2.W != 3 || sub2.H != 4 {
		t.Errorf("resized sub = (%d,%d,%d,%d), want (2,2,3,4)", sub2.X, sub2.Y, sub2.W, sub2.H)
	}
}

// TestSetWellView_Persists: a well's preview framing persists across GetGrid,
// so descent restores the same view (preview = descent target = ascent return).
func TestSetWellView_Persists(t *testing.T) {
	dir := tempTree(t)
	p := openPlugin(t)
	att, _ := p.Attach(context.Background(), &gridwellv1.AttachRequest{Config: map[string]string{"path": dir}})
	r, _ := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: att.RootGridId})
	sub := tileByName(t, r.Tiles, "sub")

	if _, err := p.SetWellView(context.Background(), &gridwellv1.SetWellViewRequest{TileId: sub.Id, ViewX: 6, ViewY: 8, ViewZoom: 2.5}); err != nil {
		t.Fatalf("SetWellView: %v", err)
	}
	r2, _ := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: att.RootGridId})
	sub2 := tileByName(t, r2.Tiles, "sub")
	if sub2.ViewX != 6 || sub2.ViewY != 8 || sub2.ViewZoom != 2.5 {
		t.Errorf("well view = (%d,%d,%v), want (6,8,2.5)", sub2.ViewX, sub2.ViewY, sub2.ViewZoom)
	}
}

// TestMoveTile_CrossGridRejected: moving a tile to a different grid is not
// supported (it would require an on-disk mv) and must error rather than
// silently corrupt placement.
func TestMoveTile_CrossGridRejected(t *testing.T) {
	dir := tempTree(t)
	p := openPlugin(t)
	att, _ := p.Attach(context.Background(), &gridwellv1.AttachRequest{Config: map[string]string{"path": dir}})
	r, _ := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: att.RootGridId})
	note := tileByName(t, r.Tiles, "note.txt")
	_, err := p.MoveTile(context.Background(), &gridwellv1.MoveTileRequest{TileId: note.Id, DestGridId: "999999", X: 1, Y: 1})
	if err == nil {
		t.Error("expected error for cross-grid move, got nil")
	}
}

// TestGetTileContent_FileMetadata: a file tile's content is a markdown
// summary of the file's metadata (parity with the legacy source reconciler);
// a directory tile has empty content.
func TestGetTileContent_FileMetadata(t *testing.T) {
	dir := tempTree(t)
	p := openPlugin(t)
	att, _ := p.Attach(context.Background(), &gridwellv1.AttachRequest{Config: map[string]string{"path": dir}})
	r, _ := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: att.RootGridId})

	note := tileByName(t, r.Tiles, "note.txt")
	resp, err := p.GetTileContent(context.Background(), &gridwellv1.GetTileContentRequest{TileId: note.Id})
	if err != nil {
		t.Fatalf("GetTileContent: %v", err)
	}
	if !strings.Contains(string(resp.Data), "note.txt") {
		t.Errorf("content %q does not mention the file name", resp.Data)
	}
	if resp.MediaType != "text/markdown" {
		t.Errorf("media_type = %q, want text/markdown", resp.MediaType)
	}

	sub := tileByName(t, r.Tiles, "sub")
	dresp, err := p.GetTileContent(context.Background(), &gridwellv1.GetTileContentRequest{TileId: sub.Id})
	if err != nil {
		t.Fatalf("GetTileContent dir: %v", err)
	}
	if len(dresp.Data) != 0 {
		t.Errorf("directory tile content = %q, want empty", dresp.Data)
	}
}

// TestAttach_DefaultsToConfiguredRoot: with no path in the Attach config, the
// plugin falls back to its configured root (the launcher-mount path).
func TestAttach_DefaultsToConfiguredRoot(t *testing.T) {
	dir := tempTree(t)
	p := openPlugin(t)
	p.SetRoot(dir)
	resp, err := p.Attach(context.Background(), &gridwellv1.AttachRequest{Config: nil})
	if err != nil {
		t.Fatalf("Attach with no path: %v", err)
	}
	if resp.RootGridId == "" {
		t.Error("RootGridId empty")
	}
	r, err := p.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: resp.RootGridId})
	if err != nil {
		t.Fatalf("GetGrid: %v", err)
	}
	if len(r.Tiles) != 2 { // note.txt + sub/
		t.Errorf("default-root grid has %d tiles, want 2 (the temp tree)", len(r.Tiles))
	}
}
