package store

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// schemaColumns returns the ordered column names of a table in the given
// attached schema ("main" or "cache").
func schemaColumns(t *testing.T, s *Store, schema, table string) []string {
	t.Helper()
	rows, err := s.db.QueryContext(context.Background(),
		"PRAGMA "+schema+".table_info("+table+")")
	if err != nil {
		t.Fatalf("table_info %s.%s: %v", schema, table, err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var (
			cid       int
			name      string
			ctype     string
			notnull   int
			dfltValue any
			pk        int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		cols = append(cols, name)
	}
	sort.Strings(cols)
	return cols
}

// TestCacheSchemaMatchesMain asserts the grids/tiles/blobs tables are
// materialized identically in the attached cache database and the durable
// main one — they share the same DDL template, and a drift here would mean a
// projected row couldn't round-trip through the same scan code.
func TestCacheSchemaMatchesMain(t *testing.T) {
	s := newTestStore(t)
	for _, table := range []string{"grids", "tiles", "blobs"} {
		main := schemaColumns(t, s, "main", table)
		cache := schemaColumns(t, s, "cache", table)
		if len(main) == 0 {
			t.Fatalf("main.%s has no columns", table)
		}
		if len(cache) != len(main) {
			t.Fatalf("%s: cache cols %v != main cols %v", table, cache, main)
		}
		for i := range main {
			if cache[i] != main[i] {
				t.Errorf("%s col %d: cache %q != main %q", table, i, cache[i], main[i])
			}
		}
	}
}

// TestCacheIDsSeededAboveBase confirms a freshly-inserted cache row gets an id
// at or above cacheIDBase, so cache ids never collide with main ids.
func TestCacheIDsSeededAboveBase(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO cache.grids (object_id, created_at) VALUES ('probe', 0)`)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if id < cacheIDBase {
		t.Errorf("cache grid id = %d, want >= %d", id, cacheIDBase)
	}
	if !isCacheID(id) {
		t.Errorf("isCacheID(%d) = false, want true", id)
	}
}

// TestSourceGridLivesInCacheOnly confirms a file-well's backing source grid is
// materialized in the ephemeral cache database, never in the durable main one
// — so the main file stays a clean archive of authored content.
func TestSourceGridLivesInCacheOnly(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	fw, err := s.CreateFileWell(ctx, &rpc.CreateFileWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1, FSPath: "/etc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !isCacheID(fw.ChildGridID) {
		t.Errorf("file-well child grid %d not in cache (>= %d)", fw.ChildGridID, int64(cacheIDBase))
	}
	// The durable main DB must hold no source grid.
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM main.grids WHERE source_kind IS NOT NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("main DB has %d source grid(s); they must live only in cache", n)
	}
}

// TestArchiveSurvivesCacheWipe is the archival guarantee: copy gridwell.db to a
// machine with no cache file (or just delete the cache), and the canvas still
// opens — exit wells re-resolve their disposable source grids on Open.
func TestArchiveSurvivesCacheWipe(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "gridwell.db")
	cachePath := cacheDBPath(mainPath)
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
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Wipe the cache file and its WAL sidecars — the "fresh machine" case.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(cachePath + suffix)
	}

	s2, err := Open(mainPath)
	if err != nil {
		t.Fatalf("reopen with wiped cache: %v", err)
	}
	defer s2.Close()

	// The durable file-well survived, and Open rebound its child grid into the
	// freshly-recreated cache so descent works.
	tile, err := s2.GetTile(ctx, fwID)
	if err != nil {
		t.Fatalf("file-well gone after cache wipe: %v", err)
	}
	if tile.Kind != rpc.KindFileWell || tile.FSPath != "/etc" {
		t.Fatalf("file-well corrupted: kind=%q fs_path=%q", tile.Kind, tile.FSPath)
	}
	if !isCacheID(tile.ChildGridID) {
		t.Fatalf("child grid %d not rebound to cache", tile.ChildGridID)
	}
	g, err := s2.GetGrid(ctx, tile.ChildGridID)
	if err != nil {
		t.Fatalf("descend into rebound source grid: %v", err)
	}
	if g.Grid.SourceKind != rpc.GridSourceFS || g.Grid.SourceID != "/etc" {
		t.Errorf("rebound grid source = (%q,%q), want (fs,/etc)", g.Grid.SourceKind, g.Grid.SourceID)
	}
}
