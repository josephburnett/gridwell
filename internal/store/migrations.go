package store

import (
	"database/sql"
	"fmt"
	"strings"
)

// applyMigrations brings an existing DB up to the current schema.
// `Schema`'s `CREATE TABLE IF NOT EXISTS` covers fresh DBs; this is the
// catch-up path for databases that pre-date a column being added.
//
// Each migration is idempotent: checks PRAGMA table_info first so
// running twice is safe.
func applyMigrations(db *sql.DB) error {
	if err := addColumnIfMissing(db, "tiles", "alt_text", "TEXT"); err != nil {
		return err
	}
	// grids gain (source_kind, source_id) for fs/proc-backed grids.
	if err := addColumnIfMissing(db, "grids", "source_kind", "TEXT"); err != nil {
		return err
	}
	if err := addColumnIfMissing(db, "grids", "source_id", "TEXT"); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_grids_source ON grids(source_kind, source_id) WHERE source_kind IS NOT NULL`); err != nil {
		return fmt.Errorf("create idx_grids_source: %w", err)
	}
	// tiles gain fs_path / pid / fs_name for the new exit-well kinds.
	if err := addColumnIfMissing(db, "tiles", "fs_path", "TEXT"); err != nil {
		return err
	}
	if err := addColumnIfMissing(db, "tiles", "pid", "INTEGER"); err != nil {
		return err
	}
	if err := addColumnIfMissing(db, "tiles", "fs_name", "TEXT"); err != nil {
		return err
	}
	// Widen the tiles.kind CHECK to admit 'file-well' and 'process-well'.
	// SQLite can't alter CHECK in place, so we rebuild the table when the
	// old constraint is still present.
	if err := rebuildTilesTableIfNeeded(db); err != nil {
		return err
	}
	// One-shot reset: an earlier proc-grid reconciler inserted the
	// synthetic "@info" tile at 2x1 instead of the 1x1 every other tile
	// uses. Tiles the user manually resized to any other dimensions are
	// preserved — we only touch rows still sitting on the old default.
	if _, err := db.Exec(
		`UPDATE tiles SET w = 1, h = 1 WHERE fs_name = '@info' AND w = 2 AND h = 1`,
	); err != nil {
		return fmt.Errorf("reset stale @info tile size: %w", err)
	}
	return nil
}

// addColumnIfMissing runs `ALTER TABLE ADD COLUMN` when the column
// isn't already present. SQLite has no `IF NOT EXISTS` form for ADD
// COLUMN, so we check via pragma_table_info first.
func addColumnIfMissing(db *sql.DB, table, col, typ string) error {
	var n int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`,
		table, col,
	).Scan(&n)
	if err != nil {
		return fmt.Errorf("check %s.%s: %w", table, col, err)
	}
	if n > 0 {
		return nil
	}
	stmt := fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, table, col, typ)
	if _, err := db.Exec(stmt); err != nil {
		return fmt.Errorf("add %s.%s: %w", table, col, err)
	}
	return nil
}

// rebuildTilesTableIfNeeded inspects the live CREATE TABLE SQL for tiles
// and, if it still names the old four-kind CHECK, rebuilds the table
// with the wider six-kind CHECK and relaxed text-tile constraint (text
// tiles can now have NULL blob_id when they're file-backed).
func rebuildTilesTableIfNeeded(db *sql.DB) error {
	var createSQL string
	err := db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'tiles'`,
	).Scan(&createSQL)
	if err != nil {
		return fmt.Errorf("read tiles schema: %w", err)
	}
	if strings.Contains(createSQL, "'file-well'") {
		return nil // already on the new schema
	}
	// SQLite recommends running the rebuild with foreign_keys=OFF to
	// avoid spurious cascading deletes during the table swap.
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer db.Exec(`PRAGMA foreign_keys=ON`)
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(tilesRebuildNewCreate); err != nil {
		return fmt.Errorf("create tiles_new: %w", err)
	}
	if _, err := tx.Exec(`
		INSERT INTO tiles_new (
		    id, object_id, version, grid_id, kind, x, y, w, h,
		    view_x, view_y, view_zoom, child_grid_id,
		    text_x, text_y, text_w, text_h, text_mode, blob_id,
		    url_string, preview_jpeg,
		    fs_path, pid, fs_name,
		    alt_text, created_at, updated_at
		)
		SELECT
		    id, object_id, version, grid_id, kind, x, y, w, h,
		    view_x, view_y, view_zoom, child_grid_id,
		    text_x, text_y, text_w, text_h, text_mode, blob_id,
		    url_string, preview_jpeg,
		    fs_path, pid, fs_name,
		    alt_text, created_at, updated_at
		FROM tiles`); err != nil {
		return fmt.Errorf("copy tiles rows: %w", err)
	}
	for _, q := range []string{
		`DROP TABLE tiles`,
		`ALTER TABLE tiles_new RENAME TO tiles`,
		`CREATE INDEX IF NOT EXISTS idx_tiles_grid_id   ON tiles(grid_id)`,
		`CREATE INDEX IF NOT EXISTS idx_tiles_object_id ON tiles(object_id)`,
		`CREATE INDEX IF NOT EXISTS idx_tiles_child     ON tiles(child_grid_id)`,
	} {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("%s: %w", q, err)
		}
	}
	return tx.Commit()
}

// tilesRebuildNewCreate is the CREATE TABLE for the post-migration tiles
// schema. Kept in sync with Schema's tiles block; the migration only
// runs once per DB so duplication is bounded.
const tilesRebuildNewCreate = `
CREATE TABLE tiles_new (
    id            INTEGER PRIMARY KEY,
    object_id     TEXT NOT NULL,
    version       INTEGER NOT NULL DEFAULT 0,
    grid_id       INTEGER NOT NULL REFERENCES grids(id),
    kind          TEXT NOT NULL CHECK (kind IN ('well','text','url','blackhole','file-well','process-well')),
    x             INTEGER NOT NULL,
    y             INTEGER NOT NULL,
    w             INTEGER NOT NULL DEFAULT 1 CHECK (w > 0),
    h             INTEGER NOT NULL DEFAULT 1 CHECK (h > 0),
    view_x        INTEGER NOT NULL DEFAULT 0,
    view_y        INTEGER NOT NULL DEFAULT 0,
    view_zoom     REAL NOT NULL DEFAULT 0,
    child_grid_id INTEGER REFERENCES grids(id),
    text_x        INTEGER NOT NULL DEFAULT 0,
    text_y        INTEGER NOT NULL DEFAULT 0,
    text_w        INTEGER NOT NULL DEFAULT 0,
    text_h        INTEGER NOT NULL DEFAULT 0,
    text_mode     TEXT,
    blob_id       INTEGER REFERENCES blobs(id),
    url_string    TEXT,
    preview_jpeg  BLOB,
    fs_path       TEXT,
    pid           INTEGER,
    fs_name       TEXT,
    alt_text      TEXT,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    CHECK (
       (kind = 'well'         AND child_grid_id IS NOT NULL AND blob_id IS NULL     AND url_string IS NULL     AND preview_jpeg IS NULL AND text_mode IS NULL AND fs_path IS NULL AND pid IS NULL)
    OR (kind = 'text'         AND child_grid_id IS NULL     AND url_string IS NULL  AND preview_jpeg IS NULL   AND fs_path IS NULL      AND pid IS NULL)
    OR (kind = 'url'          AND child_grid_id IS NULL     AND blob_id IS NULL     AND url_string IS NOT NULL AND text_mode IS NULL    AND fs_path IS NULL AND pid IS NULL)
    OR (kind = 'blackhole'    AND child_grid_id IS NULL     AND blob_id IS NULL     AND url_string IS NULL     AND preview_jpeg IS NULL AND text_mode IS NULL AND fs_path IS NULL AND pid IS NULL)
    OR (kind = 'file-well'    AND child_grid_id IS NOT NULL AND blob_id IS NULL     AND url_string IS NULL     AND preview_jpeg IS NULL AND text_mode IS NULL AND fs_path IS NOT NULL AND pid IS NULL)
    OR (kind = 'process-well' AND child_grid_id IS NOT NULL AND blob_id IS NULL     AND url_string IS NULL     AND preview_jpeg IS NULL AND text_mode IS NULL AND fs_path IS NULL AND pid IS NOT NULL)
    )
)`
