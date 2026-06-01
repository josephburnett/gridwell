package store

import (
	"database/sql"
	"fmt"
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
