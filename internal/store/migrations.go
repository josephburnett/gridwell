package store

import (
	"context"
	"database/sql"
	"fmt"
)

// applicationID marks a database file as a Gridwell DB. It is written into
// the SQLite header via PRAGMA application_id (the bytes "GWeL", big-endian
// ASCII) so file(1), archival tooling, and our own open path can recognize
// the file without reading a single row. See docs/storage-format.md.
const applicationID = 0x4757654C // "GWeL"

// schemaVersion is the schema generation this binary materializes. It is
// recorded in the SQLite header via PRAGMA user_version — the canonical,
// table-free place for an application schema version. Open stamps it on a
// fresh DB and refuses to open a DB stamped NEWER than this binary knows
// about (an older binary against a future-schema DB would misread rows).
//
// Pre-release testing mode: there is no historical migration list yet. Every
// schema change goes straight into schema.go and the user deletes the DB on
// disk (see CLAUDE.md "Testing mode — no backward compatibility yet"). When
// testing mode ends and the format is frozen, each additive change becomes
// one entry in `migrations` that bumps schemaVersion.
const schemaVersion = 1

// migration is one additive, non-destructive step that brings a DB from
// version to-1 up to version to. Migrations must only add columns/tables
// (with defaults) — never drop, rename, or repurpose existing columns, so
// data written under any prior version stays readable forever.
type migration struct {
	to  int
	run func(ctx context.Context, tx *sql.Tx) error
}

// migrations is the ordered list of post-freeze schema migrations. Empty in
// testing mode (clean breaks instead). The framework runs so the discipline
// is in place before the backward-compatibility promise is made.
var migrations []migration

// applyMigrations enforces the on-disk format contract at Open time:
//   - Fresh DB (application_id and user_version both 0): stamp the Gridwell
//     application_id and the current schemaVersion. Schema already
//     materializes the latest shape, so there is nothing to migrate.
//   - Foreign DB (application_id set but not ours): refuse — not our file.
//   - Newer DB (user_version > schemaVersion): refuse — an older binary must
//     not misread a newer schema.
//   - Older DB (user_version < schemaVersion): run each pending migration in
//     order, then stamp schemaVersion.
func (s *Store) applyMigrations(ctx context.Context) error {
	appID, err := readPragmaInt(ctx, s.db, "application_id")
	if err != nil {
		return err
	}
	userVer, err := readPragmaInt(ctx, s.db, "user_version")
	if err != nil {
		return err
	}

	if appID == 0 && userVer == 0 {
		if err := s.setPragmaInt(ctx, "application_id", applicationID); err != nil {
			return err
		}
		return s.setPragmaInt(ctx, "user_version", schemaVersion)
	}
	if appID != applicationID {
		return fmt.Errorf("not a Gridwell database: application_id %#x", appID)
	}
	if userVer > schemaVersion {
		return fmt.Errorf("stored schema version %d is newer than this binary's %d", userVer, schemaVersion)
	}
	if userVer == schemaVersion {
		return nil
	}

	err = s.withTx(ctx, func(tx *sql.Tx) error {
		for _, m := range migrations {
			if m.to <= int(userVer) || m.to > schemaVersion {
				continue
			}
			if err := m.run(ctx, tx); err != nil {
				return fmt.Errorf("migration to v%d: %w", m.to, err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	// user_version is a header field set outside the migration transaction;
	// with a single connection (MaxOpenConns(1)) it lands on the same file.
	return s.setPragmaInt(ctx, "user_version", schemaVersion)
}

// readPragmaInt reads an integer-valued PRAGMA (e.g. application_id,
// user_version) from the main database.
func readPragmaInt(ctx context.Context, q gridReader, name string) (int64, error) {
	var v int64
	// PRAGMA names are trusted literals here, never user input.
	if err := q.QueryRowContext(ctx, "PRAGMA "+name).Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}

// setPragmaInt writes an integer-valued PRAGMA. PRAGMA statements can't bind
// parameters, so the value is formatted in; callers pass trusted constants.
func (s *Store) setPragmaInt(ctx context.Context, name string, v int64) error {
	_, err := s.db.ExecContext(ctx, fmt.Sprintf("PRAGMA %s = %d", name, v))
	return err
}
