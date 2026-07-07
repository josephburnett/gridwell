package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/josephburnett/gridwell/internal/dbformat"
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
const schemaVersion = 3

// migration is one additive, non-destructive step that brings a DB from
// version to-1 up to version to. Migrations must only add columns/tables
// (with defaults) — never drop, rename, or repurpose existing columns, so
// data written under any prior version stays readable forever.
type migration struct {
	to  int
	run func(ctx context.Context, tx *sql.Tx) error
}

// migrations is the ordered list of additive post-v1 schema migrations; entry
// i brings a DB from version i+1 to i+2. The forward-compatibility promise is
// in effect — see the schemaVersion doc above and internal/store/CLAUDE.md.
var migrations = []migration{
	// v2: alt_user marks alt_text as user-owned (the rename gesture, issue
	// #61) so automatic captures never overwrite a user-set name.
	{to: 2, run: addColumnDDL(`ALTER TABLE tiles ADD COLUMN alt_user INTEGER NOT NULL DEFAULT 0`)},
	// v3: content_zoom — per-tile content scale (issue #82), framing.
	{to: 3, run: addColumnDDL(`ALTER TABLE tiles ADD COLUMN content_zoom REAL NOT NULL DEFAULT 0`)},
}

// addColumnDDL builds a migration run-func executing one additive DDL
// statement (the production twin of the test harness's addColumn).
func addColumnDDL(ddl string) func(ctx context.Context, tx *sql.Tx) error {
	return func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, ddl)
		return err
	}
}

// applyMigrations enforces the on-disk format contract at Open time, bringing
// the DB up to this binary's schemaVersion using the canonical migration list.
func (s *Store) applyMigrations(ctx context.Context) error {
	return s.migrateUp(ctx, migrations, schemaVersion)
}

// migrateUp brings the DB from its stored user_version up to target by running
// the pending entries of migs. The engine itself — fresh-stamp / foreign-file
// refusal / newer-version refusal / additive chain — lives in
// internal/dbformat.EnsureVersion, shared by EVERY plugin DB (localdb, fs,
// proc): one implementation of the format contract, no copies. The fresh-DB
// stamp shortcut is sound here because a fresh Open materializes
// tablesTemplate and TestSchemaEquivalence proves that shape equals tablesV1 +
// the full chain.
//
// migs and target are parameters (not the globals directly) so the engine can
// be exercised by tests with a synthetic chain against a frozen-v1 DB.
func (s *Store) migrateUp(ctx context.Context, migs []migration, target int) error {
	chain := make([]dbformat.Migration, 0, len(migs))
	for _, m := range migs {
		chain = append(chain, dbformat.Migration{To: m.to, Run: m.run})
	}
	return dbformat.EnsureVersion(ctx, s.db, applicationID, target, chain)
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
