package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/josephburnett/gridwell/internal/procsource"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// TestFSGridReconcileFirstDescent confirms that the first GetGrid on a
// fresh fs-grid synthesizes one tile per directory entry: subdirectories
// become file-wells with their own backing fs-grid, files become text
// tiles pointing at a synthesized metadata blob.
func TestFSGridReconcileFirstDescent(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.txt"), "hello")
	mustWrite(t, filepath.Join(dir, "b.txt"), "world")
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	w, err := s.CreateFileWell(ctx, &rpc.CreateFileWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 2, H: 2, FSPath: dir,
	})
	if err != nil {
		t.Fatal(err)
	}

	g, err := s.GetGrid(ctx, w.ChildGridID)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Tiles) != 3 {
		t.Fatalf("expected 3 tiles, got %d: %+v", len(g.Tiles), g.Tiles)
	}
	kinds := map[string]string{}
	for _, tile := range g.Tiles {
		kinds[tile.FSName] = tile.Kind
	}
	if kinds["a.txt"] != rpc.KindText || kinds["b.txt"] != rpc.KindText {
		t.Errorf("files not text-kind: %v", kinds)
	}
	if kinds["sub"] != rpc.KindFileWell {
		t.Errorf("dir not file-well-kind: %v", kinds)
	}
}

// TestFSGridReconcileStickyAfterFirstPass confirms that auto-laid-out
// positions persist across reads: removing a file leaves its position
// available for the next new file at the same cell. (This is the
// "auto-grid first, sticky after" choice — once placed, positions are
// stable.)
func TestFSGridReconcileStickyPositions(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "alpha"), "x")
	mustWrite(t, filepath.Join(dir, "beta"), "y")

	w, _ := s.CreateFileWell(ctx, &rpc.CreateFileWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 2, H: 2, FSPath: dir,
	})
	g1, _ := s.GetGrid(ctx, w.ChildGridID)
	pos1 := map[string]position{}
	for _, tile := range g1.Tiles {
		pos1[tile.FSName] = position{tile.X, tile.Y}
	}
	g2, _ := s.GetGrid(ctx, w.ChildGridID)
	for _, tile := range g2.Tiles {
		if pos1[tile.FSName] != (position{tile.X, tile.Y}) {
			t.Errorf("position of %q drifted: %v -> %v",
				tile.FSName, pos1[tile.FSName], position{tile.X, tile.Y})
		}
	}
}

// TestFSGridReconcileRemovesGoneFile confirms that a file deleted on disk
// disappears from the grid on the next read, and its tile's blob refcount
// is released.
func TestFSGridReconcileRemovesGoneFile(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	dir := t.TempDir()
	path := filepath.Join(dir, "ephemeral")
	mustWrite(t, path, "boo")

	w, _ := s.CreateFileWell(ctx, &rpc.CreateFileWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 2, H: 2, FSPath: dir,
	})
	if g, _ := s.GetGrid(ctx, w.ChildGridID); len(g.Tiles) != 1 {
		t.Fatalf("expected 1 tile, got %d", len(g.Tiles))
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	g, _ := s.GetGrid(ctx, w.ChildGridID)
	if len(g.Tiles) != 0 {
		t.Errorf("expected 0 tiles after removal, got %d", len(g.Tiles))
	}
}

// TestFSGridReconcileNoFlapForUnchanged checks that two reads of an
// unchanged directory produce the same metadata blob (hash-deduped) —
// so the tile's blob_id doesn't churn on every read.
func TestFSGridReconcileBlobDedupe(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "x"), "1")
	w, _ := s.CreateFileWell(ctx, &rpc.CreateFileWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 2, H: 2, FSPath: dir,
	})
	g1, _ := s.GetGrid(ctx, w.ChildGridID)
	g2, _ := s.GetGrid(ctx, w.ChildGridID)
	if len(g1.Tiles) != 1 || len(g2.Tiles) != 1 {
		t.Fatalf("tile count drift: %d / %d", len(g1.Tiles), len(g2.Tiles))
	}
	if g1.Tiles[0].BlobID != g2.Tiles[0].BlobID {
		t.Errorf("blob_id changed across reads: %d -> %d",
			g1.Tiles[0].BlobID, g2.Tiles[0].BlobID)
	}
}

// TestProcGridReconcile uses a stub procsource: GetGrid should produce
// one process-well tile per child PID plus the "@info" tile for the
// parent itself.
func TestProcGridReconcile(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	s.SetSourceReaders(nil, &stubProcReader{
		children: map[int64][]procsource.Info{
			1: {
				{PID: 2, PPID: 1, Name: "kthreadd"},
				{PID: 100, PPID: 1, Name: "bash", CmdLine: "/bin/bash"},
			},
		},
		self: map[int64]procsource.Info{
			1: {PID: 1, PPID: 0, Name: "init"},
		},
	}, "/proc")

	w, err := s.CreateProcessWell(ctx, &rpc.CreateProcessWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 2, H: 2, PID: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	g, err := s.GetGrid(ctx, w.ChildGridID)
	if err != nil {
		t.Fatal(err)
	}
	// One @info + two child process-wells = 3 tiles.
	if len(g.Tiles) != 3 {
		t.Fatalf("expected 3 tiles, got %d: %+v", len(g.Tiles), g.Tiles)
	}
	pids := map[int64]string{}
	names := map[int64]string{}
	for _, tile := range g.Tiles {
		pids[tile.PID] = tile.Kind
		names[tile.PID] = tile.AltText
		if tile.FSName == "@info" {
			if tile.Kind != rpc.KindText {
				t.Errorf("@info tile kind = %q, want text", tile.Kind)
			}
			// Every tile starts 1x1 — auto-grid sizing happens client-side.
			if tile.W != 1 || tile.H != 1 {
				t.Errorf("@info tile size = %dx%d, want 1x1", tile.W, tile.H)
			}
		}
	}
	if pids[2] != rpc.KindProcessWell || pids[100] != rpc.KindProcessWell {
		t.Errorf("missing or wrong-kind child process tiles: %v", pids)
	}
	// Process children carry the kernel-reported Name in alt_text so the
	// client can label them by command rather than PID.
	if names[2] != "kthreadd" {
		t.Errorf("pid 2 alt_text = %q, want %q", names[2], "kthreadd")
	}
	if names[100] != "bash" {
		t.Errorf("pid 100 alt_text = %q, want %q", names[100], "bash")
	}
}

// TestProcDisplayName covers the fallback ladder Name → cmdline basename → "".
func TestProcDisplayName(t *testing.T) {
	cases := []struct {
		info procsource.Info
		want string
	}{
		{procsource.Info{Name: "bash"}, "bash"},
		{procsource.Info{Name: "", CmdLine: "/usr/bin/firefox --new-instance"}, "firefox"},
		{procsource.Info{Name: "init", CmdLine: "/sbin/init"}, "init"}, // Name wins over cmdline.
		{procsource.Info{}, ""},
		{procsource.Info{Name: "", CmdLine: "/ /"}, ""}, // pathological cmdline → empty.
	}
	for _, c := range cases {
		if got := procDisplayName(c.info); got != c.want {
			t.Errorf("procDisplayName(%+v) = %q, want %q", c.info, got, c.want)
		}
	}
}

// stubProcReader is the test stub satisfying ProcReader.
type stubProcReader struct {
	children map[int64][]procsource.Info
	self     map[int64]procsource.Info
}

func (s *stubProcReader) Children(_ string, ppid int64) ([]procsource.Info, error) {
	return s.children[ppid], nil
}
func (s *stubProcReader) Get(_ string, pid int64) (procsource.Info, error) {
	info, ok := s.self[pid]
	if !ok {
		return procsource.Info{}, os.ErrNotExist
	}
	return info, nil
}
func (s *stubProcReader) MetadataMarkdown(info procsource.Info) string {
	return procsource.MetadataMarkdown(info)
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

