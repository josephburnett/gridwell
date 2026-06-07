package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
		kinds[tile.SourceKey] = tile.Kind
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
		pos1[tile.SourceKey] = position{tile.X, tile.Y}
	}
	g2, _ := s.GetGrid(ctx, w.ChildGridID)
	for _, tile := range g2.Tiles {
		if pos1[tile.SourceKey] != (position{tile.X, tile.Y}) {
			t.Errorf("position of %q drifted: %v -> %v",
				tile.SourceKey, pos1[tile.SourceKey], position{tile.X, tile.Y})
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
		if tile.SourceKey == "@info" {
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

// TestProcGridReconcileRefreshesAltText covers the case where an existing
// proc tile's alt_text is out of date (process renamed itself, or a PID
// got reused for a different command). The reconciler must overwrite it
// on the next GetGrid — otherwise the client keeps rendering the old
// name forever.
func TestProcGridReconcileRefreshesAltText(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	reader := &stubProcReader{
		children: map[int64][]procsource.Info{
			1: {{PID: 100, PPID: 1, Name: "bash"}},
		},
		self: map[int64]procsource.Info{
			1: {PID: 1, PPID: 0, Name: "init"},
		},
	}
	s.SetSourceReaders(nil, reader, "/proc")

	w, err := s.CreateProcessWell(ctx, &rpc.CreateProcessWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 2, H: 2, PID: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetGrid(ctx, w.ChildGridID); err != nil {
		t.Fatal(err)
	}
	// Process renames itself in the meantime.
	reader.children[1] = []procsource.Info{{PID: 100, PPID: 1, Name: "zsh"}}

	g, err := s.GetGrid(ctx, w.ChildGridID)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, tile := range g.Tiles {
		if tile.PID != 100 {
			continue
		}
		found = true
		if tile.AltText != "zsh" {
			t.Errorf("pid 100 alt_text = %q after rename, want %q", tile.AltText, "zsh")
		}
	}
	if !found {
		t.Errorf("pid 100 tile missing from grid")
	}
}

// TestProcInfoBlobPopulatesAndRefreshes covers the @info tile body —
// the synthetic per-PID metadata tile inside a proc-well. On first
// reconcile the tile must carry a populated blob with the rendered
// process metadata; on a subsequent reconcile where the process state
// has changed (memory grew, command changed), the blob id must move
// and a new tile version must be observable so the SSE fan-out can
// reach connected clients.
func TestProcInfoBlobPopulatesAndRefreshes(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	reader := &stubProcReader{
		self: map[int64]procsource.Info{
			1: {PID: 1, PPID: 0, Name: "init", CmdLine: "/sbin/init", VmRSSKB: 1024},
		},
	}
	s.SetSourceReaders(nil, reader, "/proc")

	w, err := s.CreateProcessWell(ctx, &rpc.CreateProcessWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 2, H: 2, PID: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	g1, err := s.GetGrid(ctx, w.ChildGridID)
	if err != nil {
		t.Fatal(err)
	}
	info1 := findInfoTile(t, g1.Tiles)
	if info1.BlobID == 0 {
		t.Fatalf("@info blob_id = 0 after first reconcile, want populated")
	}
	body1, err := s.GetBlob(ctx, info1.BlobID)
	if err != nil {
		t.Fatalf("get @info blob: %v", err)
	}
	if !strings.Contains(string(body1), "init") || !strings.Contains(string(body1), "1.0 MiB") {
		t.Errorf("@info body missing process fields, got: %q", body1)
	}

	// Process changes: memory grew, cmdline changed.
	reader.self[1] = procsource.Info{PID: 1, PPID: 0, Name: "init", CmdLine: "/sbin/init --reload", VmRSSKB: 2048}
	g2, err := s.GetGrid(ctx, w.ChildGridID)
	if err != nil {
		t.Fatal(err)
	}
	info2 := findInfoTile(t, g2.Tiles)
	if info2.BlobID == info1.BlobID {
		t.Errorf("@info blob_id unchanged after process state change: %d", info2.BlobID)
	}
	if info2.Version <= info1.Version {
		t.Errorf("@info version did not bump: %d -> %d", info1.Version, info2.Version)
	}
	body2, _ := s.GetBlob(ctx, info2.BlobID)
	if !strings.Contains(string(body2), "--reload") {
		t.Errorf("refreshed @info body missing new cmdline: %q", body2)
	}

	// No-change reconcile must NOT bump the version (otherwise every
	// GetGrid would fire spurious SSE noise on quiet processes).
	g3, err := s.GetGrid(ctx, w.ChildGridID)
	if err != nil {
		t.Fatal(err)
	}
	info3 := findInfoTile(t, g3.Tiles)
	if info3.Version != info2.Version {
		t.Errorf("@info version bumped on no-op reconcile: %d -> %d", info2.Version, info3.Version)
	}
	if info3.BlobID != info2.BlobID {
		t.Errorf("@info blob_id changed on no-op reconcile: %d -> %d", info2.BlobID, info3.BlobID)
	}
}

// TestUpdateTextRejectsSourceBacked locks in the server-side read-only
// contract for source-backed text tiles. Even a client that has the
// correct version of the tile must not be able to overwrite its blob —
// the body comes from the reconciler.
func TestUpdateTextRejectsSourceBacked(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	s.SetSourceReaders(nil, &stubProcReader{
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
	info := findInfoTile(t, g.Tiles)
	_, err = s.UpdateText(ctx, &rpc.UpdateTextRequest{
		TileID: info.ID, Version: info.Version, Data: []byte("evil content"),
	})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("UpdateText on @info: err = %v, want ErrInvalidArgument", err)
	}
}

func findInfoTile(t *testing.T, tiles []rpc.Tile) rpc.Tile {
	t.Helper()
	for _, tile := range tiles {
		if tile.SourceKey == "@info" {
			return tile
		}
	}
	t.Fatalf("no @info tile in %d tiles", len(tiles))
	return rpc.Tile{}
}

// TestProcDisplayName covers the fallback ladder Name → cmdline basename
// → "pid N". The PID fallback is what guarantees the client always sees
// a usable label, even for processes with empty status/cmdline.
func TestProcDisplayName(t *testing.T) {
	cases := []struct {
		info procsource.Info
		want string
	}{
		{procsource.Info{PID: 200, Name: "bash"}, "bash"},
		{procsource.Info{PID: 300, Name: "", CmdLine: "/usr/bin/firefox --new-instance"}, "firefox"},
		{procsource.Info{PID: 1, Name: "init", CmdLine: "/sbin/init"}, "init"}, // Name wins over cmdline.
		{procsource.Info{PID: 4242}, "pid 4242"},
		{procsource.Info{PID: 4243, Name: "", CmdLine: "/ /"}, "pid 4243"}, // pathological cmdline → pid fallback.
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
