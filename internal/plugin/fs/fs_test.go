package fs_test

import (
	"context"
	"os"
	"path/filepath"
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
	if resp.RootGridId <= 0 {
		t.Errorf("RootGridId: got %d, want >0", resp.RootGridId)
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
	if subTile.ChildGridId == 0 {
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

	ids1 := map[string]int64{}
	for _, tile := range r1.Tiles {
		ids1[tile.AltText] = tile.Id
	}
	for _, tile := range r2.Tiles {
		if ids1[tile.AltText] != tile.Id {
			t.Errorf("tile %q: id changed %d→%d", tile.AltText, ids1[tile.AltText], tile.Id)
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
	ids1 := map[string]int64{}
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
			t.Errorf("existing tile %q id changed: %d→%d", tile.AltText, ids1[tile.AltText], tile.Id)
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

	var noteID int64
	for _, tile := range resp.Tiles {
		if tile.AltText == "note.txt" {
			noteID = tile.Id
		}
	}
	if noteID == 0 {
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
	probeResp, err := p.Probe(context.Background(), &gridwellv1.ProbeRequest{TileId: 99999})
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

	var noteID int64
	for _, tile := range resp.Tiles {
		if tile.AltText == "note.txt" {
			noteID = tile.Id
		}
	}
	if noteID == 0 {
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

	var noteID int64
	for _, tile := range resp.Tiles {
		if tile.AltText == "note.txt" {
			noteID = tile.Id
		}
	}
	if noteID == 0 {
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

	var subID int64
	for _, tile := range resp.Tiles {
		if tile.AltText == "sub" {
			subID = tile.Id
		}
	}
	if subID == 0 {
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
	_, err := p.DeleteTile(context.Background(), &gridwellv1.DeleteTileRequest{TileId: 99999})
	if err != nil {
		t.Fatalf("DeleteTile missing: %v", err)
	}
}
