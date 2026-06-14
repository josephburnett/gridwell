package store

import (
	"context"
	"sort"
	"testing"
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
