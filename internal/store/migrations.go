package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

// schemaVersion is the version Schema currently materializes. Open
// stamps it into the system KV table on a fresh DB; on an existing DB,
// Open compares against the stored value and runs the deltas in
// `migrations` to bridge.
//
// Pre-release testing mode: `migrations` is empty. Every schema change
// goes straight into schema.go and the user is expected to delete the
// DB on disk. After testing mode ends, each additive schema change
// becomes one migration here and bumps schemaVersion by one.
const schemaVersion = 1

const systemKeySchemaVersion = "schema_version"

// migration is one step from version N-1 to version N. Version is the
// schema_version it brings the database UP TO. Apply runs inside the
// Open-time transaction.
type migration struct {
	Version int
	Apply   func(ctx context.Context, tx *sql.Tx) error
}

// migrations is the ordered list of post-Schema upgrades. Empty in
// testing mode. Each entry's Apply must be idempotent enough that an
// interrupted Open (e.g. SIGKILL between Apply and the version stamp)
// doesn't leave a wedged DB.
var migrations = []migration{}

// applyMigrations brings the stored schema_version up to schemaVersion.
// Stamps the version key on a fresh DB; applies + stamps on an existing
// one. Idempotent.
func (s *Store) applyMigrations(ctx context.Context) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		current, err := readSchemaVersion(ctx, tx)
		if err != nil {
			return err
		}
		if current > schemaVersion {
			return fmt.Errorf("stored schema_version %d is newer than this binary's %d", current, schemaVersion)
		}
		for _, m := range migrations {
			if m.Version <= current || m.Version > schemaVersion {
				continue
			}
			if err := m.Apply(ctx, tx); err != nil {
				return fmt.Errorf("apply migration v%d: %w", m.Version, err)
			}
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
// table. Returns 0 if the key is absent (fresh DB or pre-versioning DB).
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
