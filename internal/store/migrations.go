package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

// schemaVersion is the version Schema currently materializes. Open stamps
// it into the system KV table on a fresh DB.
//
// Pre-release testing mode: there is no migration framework. Every schema
// change goes straight into schema.go and the user deletes the DB on disk
// (see CLAUDE.md "Testing mode — no backward compatibility yet"). When
// testing mode ends, a real migration framework comes back here and each
// additive change becomes one migration that bumps schemaVersion.
const schemaVersion = 1

const systemKeySchemaVersion = "schema_version"

// applyMigrations stamps the schema_version on Open. In testing mode there
// are no migrations to run, so this just records the current version on a
// fresh DB and refuses to open a DB stamped NEWER than this binary knows
// about (an older binary against a future-schema DB would misread rows).
func (s *Store) applyMigrations(ctx context.Context) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		current, err := readSchemaVersion(ctx, tx)
		if err != nil {
			return err
		}
		if current > schemaVersion {
			return fmt.Errorf("stored schema_version %d is newer than this binary's %d", current, schemaVersion)
		}
		if current == schemaVersion {
			return nil
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO system (key, value) VALUES (?, ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
			systemKeySchemaVersion, strconv.Itoa(schemaVersion))
		return err
	})
}

// readSchemaVersion returns the schema_version recorded in the system
// table. Returns 0 if the key is absent (fresh DB).
func readSchemaVersion(ctx context.Context, tx *sql.Tx) (int, error) {
	var v string
	err := tx.QueryRowContext(ctx,
		`SELECT value FROM system WHERE key = ?`, systemKeySchemaVersion).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(v)
}
