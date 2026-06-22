package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// TestRebindExitWellsRebuildsSourceGrid is the portability guarantee: open a
// file-backed DB, create a file-well (source grid materialized in main DB),
// close, then manually delete the source grid row, reopen. Open must call
// rebindExitWells, which re-creates the source grid so descent still works.
func TestRebindExitWellsRebuildsSourceGrid(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "gridwell.db")
	ctx := context.Background()

	s, err := Open(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	root, err := s.RootGridID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fw, err := s.CreateFileWell(ctx, &rpc.CreateFileWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1, FSPath: "/etc",
	})
	if err != nil {
		t.Fatal(err)
	}
	fwID := fw.ID
	origChildIDInt, _ := parseID(fw.ChildGridID)

	// Manually delete the source grid to simulate a stale pointer.
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM tiles WHERE grid_id = ?`, origChildIDInt); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM grids WHERE id = ?`, origChildIDInt); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(mainPath)
	if err != nil {
		t.Fatalf("reopen with deleted source grid: %v", err)
	}
	defer s2.Close()

	// rebindExitWells must have re-created the source grid and updated child_grid_id.
	tile, err := s2.GetTile(ctx, fwID)
	if err != nil {
		t.Fatalf("file-well gone after reopen: %v", err)
	}
	if tile.Kind != rpc.KindFileWell || tile.FSPath != "/etc" {
		t.Fatalf("file-well corrupted: kind=%q fs_path=%q", tile.Kind, tile.FSPath)
	}
	if tile.ChildGridID == "" {
		t.Fatal("child_grid_id not rebound by rebindExitWells")
	}
	g, err := s2.GetGrid(ctx, tile.ChildGridID)
	if err != nil {
		t.Fatalf("descend into rebound source grid: %v", err)
	}
	if g.Grid.SourceKind != rpc.GridSourceFS || g.Grid.SourceID != "/etc" {
		t.Errorf("rebound grid source = (%q,%q), want (fs,/etc)", g.Grid.SourceKind, g.Grid.SourceID)
	}
}

// TestRebindExitWellsIdempotent checks that running rebindExitWells when all
// source grids already exist is a no-op (child_grid_id unchanged).
func TestRebindExitWellsIdempotent(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "gridwell.db")
	ctx := context.Background()

	s, err := Open(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	root, err := s.RootGridID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fw, err := s.CreateFileWell(ctx, &rpc.CreateFileWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1, FSPath: "/etc",
	})
	if err != nil {
		t.Fatal(err)
	}
	origChildID := fw.ChildGridID // string comparison for idempotency check
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	tile, err := s2.GetTile(ctx, fw.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tile.ChildGridID != origChildID {
		t.Errorf("child_grid_id changed from %s to %s (should be stable)", origChildID, tile.ChildGridID)
	}
}

// TestSourceGridLivesInMain checks that a file-well's backing source grid is
// materialized in the durable main database.
func TestSourceGridLivesInMain(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	fw, err := s.CreateFileWell(ctx, &rpc.CreateFileWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1, FSPath: "/etc",
	})
	if err != nil {
		t.Fatal(err)
	}
	var srcKind string
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(source_kind,'') FROM grids WHERE id = ?`, fw.ChildGridID).Scan(&srcKind); err != nil {
		t.Fatalf("query source grid: %v", err)
	}
	if srcKind != rpc.GridSourceFS {
		t.Errorf("source_kind = %q, want %q", srcKind, rpc.GridSourceFS)
	}
}

// TestCacheFileNotCreated verifies that Open no longer creates a *-cache.db
// beside the main database file.
func TestCacheFileNotCreated(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "gridwell.db")

	s, err := Open(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "gridwell.db" && e.Name() != "gridwell.db-wal" && e.Name() != "gridwell.db-shm" {
			t.Errorf("unexpected file in DB dir: %s (cache DB should not be created)", e.Name())
		}
	}
}
