package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/josephburnett/gridwell/internal/rpc"
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

// rebindExitWells re-resolves every durable file/process well's child_grid_id
// against the cache database. The child id is a soft cross-file pointer with
// no FK; normally the cache persists and the id stays valid (a no-op here),
// but when the cache file is absent — the archival case: gridwell.db opened
// on another machine, or the cache simply deleted — the stored id is stale.
// Re-resolving by identity (fs_path / pid) recreates the (empty) source grid
// and rewrites child_grid_id so descents work; the listing is reconciled
// lazily on first GetGrid. Runs once at Open, after the cache is attached.
func (s *Store) rebindExitWells(ctx context.Context) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT id, kind, COALESCE(fs_path,''), COALESCE(pid,0), COALESCE(child_grid_id,0)
			   FROM tiles WHERE kind IN ('file-well','process-well')`)
		if err != nil {
			return err
		}
		type exitWell struct {
			id          int64
			kind        string
			fsPath      string
			pid, child  int64
		}
		var wells []exitWell
		for rows.Next() {
			var w exitWell
			if err := rows.Scan(&w.id, &w.kind, &w.fsPath, &w.pid, &w.child); err != nil {
				rows.Close()
				return err
			}
			wells = append(wells, w)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()

		now := s.now().Unix()
		for _, w := range wells {
			var sourceKind, sourceID string
			switch w.kind {
			case rpc.KindFileWell:
				sourceKind, sourceID = rpc.GridSourceFS, w.fsPath
			case rpc.KindProcessWell:
				sourceKind, sourceID = rpc.GridSourceProc, strconv.FormatInt(w.pid, 10)
			}
			if sourceID == "" {
				continue
			}
			gid, err := s.getOrCreateSourceGrid(ctx, tx, sourceKind, sourceID, now)
			if err != nil {
				return err
			}
			if gid != w.child {
				if _, err := tx.ExecContext(ctx,
					`UPDATE tiles SET child_grid_id = ? WHERE id = ?`, gid, w.id); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
