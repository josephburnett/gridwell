package sourcecache

import (
	"path/filepath"
	"testing"
)

// A cache file from before the door and workspace spellings opens empty: every
// answer it held spelled an old kind, and the next read rewarms it.
func TestAV1CacheOpensEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO grids (id, proto, fetched_at) VALUES ('n/1', x'00', 1)`,
		`INSERT INTO tiles (id, grid_id, proto, fetched_at) VALUES ('n/2', 'n/1', x'00', 1)`,
		`PRAGMA user_version = 1`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	s.db.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatalf("reopen a v1 cache: %v", err)
	}
	defer s.db.Close()
	for _, table := range []string{"grids", "tiles"} {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(1) FROM ` + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s holds %d rows after the v2 migration, want 0", table, n)
		}
	}
	var v int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != cacheSchemaVersion {
		t.Errorf("user_version = %d, want %d", v, cacheSchemaVersion)
	}
}
