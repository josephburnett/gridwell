package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
)

// cacheIDBase is the first autoincrement id used by the attached ephemeral
// cache database. The main and cache files share one id space (so the wasm
// client can treat every id uniformly), so the cache's autoincrement
// sequences are pre-seeded to this high base: any id >= cacheIDBase lives in
// cache, anything below in main. It sits far above any realistic main id and
// below 2^53 (the JSON/JS-safe integer ceiling the client relies on). See
// docs/storage-format.md.
const cacheIDBase = 1_000_000_000_000

// isCacheID reports whether a grid/tile/blob id belongs to the attached
// cache database rather than the durable main database.
func isCacheID(id int64) bool { return id >= cacheIDBase }

// schemaOf returns the SQL schema prefix ("" for the durable main database,
// "cache." for the attached ephemeral one) that a grid/tile/blob id lives in.
// Every id-keyed query interpolates it before the (unqualified) table name so
// the one connection routes to the right file. Because cache ids are seeded
// above cacheIDBase, the id alone says which file a row is in.
func schemaOf(id int64) string {
	if isCacheID(id) {
		return "cache."
	}
	return ""
}

// cacheDBPath derives the ephemeral cache file that sits beside the durable
// main DB: gridwell.db -> gridwell-cache.db. An in-memory main DB gets an
// independent in-memory cache (ATTACH ':memory:' opens a distinct database).
func cacheDBPath(mainPath string) string {
	if mainPath == ":memory:" || mainPath == "" {
		return ":memory:"
	}
	ext := filepath.Ext(mainPath)
	return strings.TrimSuffix(mainPath, ext) + "-cache" + ext
}

// attachCache attaches the ephemeral cache database beside the main one,
// materializes its grids/tiles/blobs tables, and seeds its autoincrement
// sequences to cacheIDBase so cache ids never collide with main ids. Runs on
// the same single connection as the main DB, so one transaction spans both
// files atomically.
func attachCache(ctx context.Context, db *sql.DB, mainPath string) error {
	cachePath := cacheDBPath(mainPath)
	// ATTACH takes a filename literal; single-quote-escape the path. (Bound
	// parameters aren't portable across all drivers for ATTACH.)
	if _, err := db.ExecContext(ctx,
		"ATTACH DATABASE '"+strings.ReplaceAll(cachePath, "'", "''")+"' AS cache"); err != nil {
		return fmt.Errorf("attach cache: %w", err)
	}
	if _, err := db.ExecContext(ctx, tablesDDL("cache.")); err != nil {
		return fmt.Errorf("cache schema: %w", err)
	}
	// Pre-seed the cache autoincrement sequences. Creating AUTOINCREMENT
	// tables materializes cache.sqlite_sequence; seeding seq = base-1 makes
	// the next inserted id exactly cacheIDBase. The NOT EXISTS guard keeps a
	// re-open from clobbering an already-advanced sequence. Table names are
	// trusted literals.
	for _, table := range []string{"grids", "tiles", "blobs"} {
		if _, err := db.ExecContext(ctx, fmt.Sprintf(
			`INSERT INTO cache.sqlite_sequence (name, seq)
			   SELECT '%s', %d
			   WHERE NOT EXISTS (SELECT 1 FROM cache.sqlite_sequence WHERE name = '%s')`,
			table, int64(cacheIDBase-1), table)); err != nil {
			return fmt.Errorf("seed cache sequence %s: %w", table, err)
		}
	}
	return nil
}
