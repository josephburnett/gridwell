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
	verifyRefcounts(t, s)
}

// TestFSGridReconcileDeleteReleasesAllRefs drives deleteFSGridTile across
// both reference kinds it must release — a regular file (text tile holding
// a blob) and a subdirectory (file-well holding a child grid) — and asserts
// no leak via verifyRefcounts. This is the safety net for the reconcile
// delete path: it used to hand-roll per-kind decrements (and ignore
// preview_blob_id) instead of going through the tileRefs source of truth.
func TestFSGridReconcileDeleteReleasesAllRefs(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "file.txt"), "hello")
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, "subdir", "inner.txt"), "deep")

	w, err := s.CreateFileWell(ctx, &rpc.CreateFileWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 3, H: 3, FSPath: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	// First reconcile materializes a text tile (file) + a file-well tile
	// (subdir, with its own descended child grid).
	g, _ := s.GetGrid(ctx, w.ChildGridID)
	if len(g.Tiles) != 2 {
		t.Fatalf("expected 2 tiles (file + subdir), got %d", len(g.Tiles))
	}
	var sub rpc.Tile
	for _, tl := range g.Tiles {
		if tl.Kind == rpc.KindFileWell {
			sub = tl
		}
	}
	if sub.ID == 0 {
		t.Fatal("no file-well tile for subdir")
	}
	// Descend into the subdir so its child grid is populated too — gives the
	// reconcile delete a non-trivial spine to release.
	if _, err := s.GetGrid(ctx, sub.ChildGridID); err != nil {
		t.Fatal(err)
	}
	verifyRefcounts(t, s)

	// Remove both entries on disk, then reconcile: deleteFSGridTile must fire
	// for the text tile (release its blob) and the file-well (release its
	// child grid).
	if err := os.RemoveAll(filepath.Join(dir, "subdir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "file.txt")); err != nil {
		t.Fatal(err)
	}
	g, _ = s.GetGrid(ctx, w.ChildGridID)
	if len(g.Tiles) != 0 {
		t.Errorf("expected 0 tiles after removal, got %d", len(g.Tiles))
	}
	verifyRefcounts(t, s)
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

// tileBySourceKey finds a reconciled tile by its source_key ("@info" or a PID
// string) in a grid response.
func tileBySourceKey(tiles []rpc.Tile, key string) (rpc.Tile, bool) {
	for _, t := range tiles {
		if t.SourceKey == key {
			return t, true
		}
	}
	return rpc.Tile{}, false
}

// TestProcGridReconcileSweepsWhenProcessesGone: when the parent process is
// *definitively* gone (absent from /proc, per Exists), the reconcile reclaims
// both @info and the now-absent children — the source grid truthfully empties,
// exactly as a deleted file's tile is swept.
func TestProcGridReconcileSweepsWhenProcessesGone(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	reader := &stubProcReader{
		children: map[int64][]procsource.Info{1: {{PID: 100, PPID: 1, Name: "bash"}}},
		self:     map[int64]procsource.Info{1: {PID: 1, PPID: 0, Name: "init"}},
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

	// Whole subtree exits: not in self, not in children, and Exists (derived
	// from self/children) reports both definitively gone.
	delete(reader.self, 1)
	reader.children[1] = nil

	g, err := s.GetGrid(ctx, w.ChildGridID)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Tiles) != 0 {
		t.Errorf("expected empty grid after the process tree exited, got %d tiles: %+v", len(g.Tiles), g.Tiles)
	}
}

// TestProcGridReconcileSweepsReparentedChildrenWhenParentGone: when the well's
// own process exits, the grid projects a dead PID's subtree, so it empties —
// even an ex-child that reparented (to init) and is still running is swept,
// because it is no longer THIS well's child.
func TestProcGridReconcileSweepsReparentedChildrenWhenParentGone(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	reader := &stubProcReader{
		children: map[int64][]procsource.Info{1: {{PID: 100, PPID: 1, Name: "daemon"}}},
		self:     map[int64]procsource.Info{1: {PID: 1, PPID: 0, Name: "init"}},
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

	// Parent (pid 1) exits; child 100 survives, reparented away from pid 1
	// (so it's no longer in Children(1)), but still present in /proc.
	delete(reader.self, 1)
	reader.children[1] = nil
	reader.exists = map[int64]bool{1: false, 100: true}

	g, err := s.GetGrid(ctx, w.ChildGridID)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Tiles) != 0 {
		t.Errorf("parent gone but grid not empty (%d tiles): reparented ex-children should be swept: %+v", len(g.Tiles), g.Tiles)
	}
}

// TestProcGridReconcilePreservesInfoOnTransientReadError is the regression for
// the over-broad "@info survives a dead process" fix: a *failed read* of a
// still-running process (Get errors, but Exists reports present) must NOT
// delete-and-reinsert @info. Doing so would re-row it (new id) and dump it at
// an auto cell — losing the placement and identity the user put on it. The
// tile must keep its exact id and position.
func TestProcGridReconcilePreservesInfoOnTransientReadError(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	reader := &stubProcReader{
		children: map[int64][]procsource.Info{1: {{PID: 100, PPID: 1, Name: "bash"}}},
		self:     map[int64]procsource.Info{1: {PID: 1, PPID: 0, Name: "init"}},
	}
	s.SetSourceReaders(nil, reader, "/proc")

	w, err := s.CreateProcessWell(ctx, &rpc.CreateProcessWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 2, H: 2, PID: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	g0, err := s.GetGrid(ctx, w.ChildGridID)
	if err != nil {
		t.Fatal(err)
	}
	info0, ok := tileBySourceKey(g0.Tiles, "@info")
	if !ok {
		t.Fatal("no @info tile after first reconcile")
	}
	// Put @info somewhere deliberate.
	if _, err := s.MoveTile(ctx, &rpc.MoveTileRequest{
		Path:       rpc.Path{WellIDs: []int64{w.ID}},
		TileID:     info0.ID,
		Version:    info0.Version,
		DestGridID: w.ChildGridID,
		DestPath:   rpc.Path{WellIDs: []int64{w.ID}},
		X:          5, Y: 5,
	}); err != nil {
		t.Fatal(err)
	}

	// Transient read failure: Get fails, but the process is still present.
	reader.getErr = map[int64]error{1: os.ErrPermission}

	g1, err := s.GetGrid(ctx, w.ChildGridID)
	if err != nil {
		t.Fatal(err)
	}
	info1, ok := tileBySourceKey(g1.Tiles, "@info")
	if !ok {
		t.Fatalf("@info was removed on a transient read error; want it preserved (tiles=%d)", len(g1.Tiles))
	}
	if info1.ID != info0.ID {
		t.Errorf("@info re-rowed on a transient error: id %d -> %d", info0.ID, info1.ID)
	}
	if info1.X != 5 || info1.Y != 5 {
		t.Errorf("@info lost its position on a transient error: (%d,%d), want (5,5)", info1.X, info1.Y)
	}
}

// TestProcGridReconcilePreservesChildOnTransientReadError covers the same rule
// for a *child* tile: Children drops any PID it couldn't read this pass, but a
// still-present process (Exists true) must keep its child tile's id and place
// rather than being swept and re-placed when it reappears in the listing.
func TestProcGridReconcilePreservesChildOnTransientReadError(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	reader := &stubProcReader{
		children: map[int64][]procsource.Info{1: {
			{PID: 100, PPID: 1, Name: "bash"},
			{PID: 200, PPID: 1, Name: "zsh"},
		}},
		self: map[int64]procsource.Info{1: {PID: 1, PPID: 0, Name: "init"}},
	}
	s.SetSourceReaders(nil, reader, "/proc")

	w, err := s.CreateProcessWell(ctx, &rpc.CreateProcessWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 2, H: 2, PID: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	g0, err := s.GetGrid(ctx, w.ChildGridID)
	if err != nil {
		t.Fatal(err)
	}
	child0, ok := tileBySourceKey(g0.Tiles, "200")
	if !ok {
		t.Fatal("no child tile for pid 200 after first reconcile")
	}
	if _, err := s.MoveTile(ctx, &rpc.MoveTileRequest{
		Path:       rpc.Path{WellIDs: []int64{w.ID}},
		TileID:     child0.ID,
		Version:    child0.Version,
		DestGridID: w.ChildGridID,
		DestPath:   rpc.Path{WellIDs: []int64{w.ID}},
		X:          6, Y: 6,
	}); err != nil {
		t.Fatal(err)
	}

	// pid 200 becomes unreadable this pass (dropped from Children) but is still
	// present in /proc (Exists true for all three).
	reader.children[1] = []procsource.Info{{PID: 100, PPID: 1, Name: "bash"}}
	reader.exists = map[int64]bool{1: true, 100: true, 200: true}

	g1, err := s.GetGrid(ctx, w.ChildGridID)
	if err != nil {
		t.Fatal(err)
	}
	child1, ok := tileBySourceKey(g1.Tiles, "200")
	if !ok {
		t.Fatalf("child pid 200 was swept while still running; want it preserved (tiles=%d)", len(g1.Tiles))
	}
	if child1.ID != child0.ID {
		t.Errorf("child 200 re-rowed on a transient error: id %d -> %d", child0.ID, child1.ID)
	}
	if child1.X != 6 || child1.Y != 6 {
		t.Errorf("child 200 lost its position on a transient error: (%d,%d), want (6,6)", child1.X, child1.Y)
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
//
//   - getErr[pid], when set, forces Get to fail with that error (a
//     transient/permission read failure that is NOT a "process gone" signal).
//   - exists, when non-nil, is the authoritative presence map Exists reads;
//     when nil, presence is derived from self / children (anything we have
//     metadata for is present). This lets a test model "alive but unreadable"
//     (Get fails / not in children, yet Exists reports present).
type stubProcReader struct {
	children map[int64][]procsource.Info
	self     map[int64]procsource.Info
	getErr   map[int64]error
	exists   map[int64]bool
}

func (s *stubProcReader) Children(_ string, ppid int64) ([]procsource.Info, error) {
	return s.children[ppid], nil
}
func (s *stubProcReader) Get(_ string, pid int64) (procsource.Info, error) {
	if err, ok := s.getErr[pid]; ok {
		return procsource.Info{}, err
	}
	info, ok := s.self[pid]
	if !ok {
		return procsource.Info{}, os.ErrNotExist
	}
	return info, nil
}
func (s *stubProcReader) Exists(_ string, pid int64) (bool, error) {
	if s.exists != nil {
		return s.exists[pid], nil
	}
	if _, ok := s.self[pid]; ok {
		return true, nil
	}
	for _, kids := range s.children {
		for _, k := range kids {
			if k.PID == pid {
				return true, nil
			}
		}
	}
	return false, nil
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
