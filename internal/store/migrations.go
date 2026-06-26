package store

import (
	"context"
	"database/sql"
	"fmt"
)

// applicationID marks a database file as a Gridwell DB. It is written into
// the SQLite header via PRAGMA application_id (the bytes "GWeL", big-endian
// ASCII) so file(1), archival tooling, and our own open path can recognize
// the file without reading a single row. See internal/store/CLAUDE.md.
const applicationID = 0x4757654C // "GWeL"

// schemaVersion is the schema generation this binary materializes. It is
// recorded in the SQLite header via PRAGMA user_version — the canonical,
// table-free place for an application schema version. Open stamps it on a
// fresh DB and refuses to open a DB stamped NEWER than this binary knows
// about (an older binary against a future-schema DB would misread rows).
//
// Forward-compatibility guarantee — IN EFFECT for the localdb plugin. v1 is
// frozen (see tablesV1 in schema.go). Data written by any released binary
// stays readable forever; never delete the DB to absorb a schema change.
// Every change is additive — an ALTER TABLE ADD COLUMN, or a new table/index —
// that bumps schemaVersion by exactly one and appends one entry to `migrations`
// (plus one test fixture). tablesTemplate (schema.go) stays the readable latest
// shape; TestSchemaEquivalence proves a fresh Open equals tablesV1 + the full
// chain, which is what makes the fresh-DB stamp shortcut in applyMigrations
// sound. See internal/store/CLAUDE.md for the full contract.
const schemaVersion = 1

// migration is one additive, non-destructive step that brings a DB from
// version to-1 up to version to. Migrations must only add columns/tables
// (with defaults) — never drop, rename, or repurpose existing columns, so
// data written under any prior version stays readable forever.
type migration struct {
	to  int
	run func(ctx context.Context, tx *sql.Tx) error
}

// migrations is the ordered list of additive post-v1 schema migrations; entry
// i brings a DB from version i+1 to i+2. Empty until the first post-v1 change.
// The forward-compatibility promise is now in effect — see the schemaVersion
// doc above and internal/store/CLAUDE.md.
var migrations []migration

// applyMigrations enforces the on-disk format contract at Open time, bringing
// the DB up to this binary's schemaVersion using the canonical migration list.
func (s *Store) applyMigrations(ctx context.Context) error {
	return s.migrateUp(ctx, migrations, schemaVersion)
}

// migrateUp brings the DB from its stored user_version up to target by running
// the pending entries of migs. It is the whole on-disk format contract:
//   - Fresh DB (application_id and user_version both 0): stamp the Gridwell
//     application_id and target. A fresh Open already materializes the latest
//     shape (tablesTemplate), so there is nothing to migrate — and
//     TestSchemaEquivalence proves that shape equals tablesV1 + the full chain,
//     which is what makes this shortcut sound.
//   - Foreign DB (application_id set but not ours): refuse — not our file.
//   - Newer DB (user_version > target): refuse — an older binary must not
//     misread a newer schema.
//   - Older DB (user_version < target): run each pending migration in order in
//     one transaction, then stamp target.
//
// migs and target are parameters (not the globals directly) so the real engine
// can be exercised by tests with a synthetic chain against a frozen-v1 DB.
func (s *Store) migrateUp(ctx context.Context, migs []migration, target int) error {
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
		return s.setPragmaInt(ctx, "user_version", int64(target))
	}
	if appID != applicationID {
		return fmt.Errorf("not a Gridwell database: application_id %#x", appID)
	}
	if userVer > int64(target) {
		return fmt.Errorf("stored schema version %d is newer than this binary's %d", userVer, target)
	}
	if userVer == int64(target) {
		return nil
	}

	err = s.withTx(ctx, func(tx *sql.Tx) error {
		for _, m := range migs {
			if m.to <= int(userVer) || m.to > target {
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
	return s.setPragmaInt(ctx, "user_version", int64(target))
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
