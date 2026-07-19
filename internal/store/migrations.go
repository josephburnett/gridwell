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
const schemaVersion = 6

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
	// v4: url_history — a url tile's navigation back-stack across
	// freeze/revive (issue #113).
	{to: 4, run: addColumnDDL(`ALTER TABLE tiles ADD COLUMN url_history TEXT`)},
	// v5: the 'pane' tile kind (durable workspaces). A kind lives in the
	// tiles table-level CHECK, which ALTER TABLE cannot touch — this is the
	// chain's first table-REBUILD migration (the recipe in
	// internal/store/CLAUDE.md). No column changes; only the CHECK.
	{to: 5, run: rebuildTilesForPaneKind},
	// v6: link_target_id — the leaf-link variant (a text/url/shell/pane tile
	// whose content lives in another plugin's tile; owner decision
	// 2026-07-19, cross-plugin left-drag = link). The CHECK gains the link
	// branch (and a url link row has url_string NULL, which the v5 url
	// branch forbade), so this is a rebuild, not an ADD COLUMN. Old rows
	// get link_target_id NULL (not a link) — exactly their old meaning.
	{to: 6, run: rebuildTilesForLinkTarget},
}

// tilesRebuildColumns is the explicit column list a rebuild copies — every
// tiles column as of v5, id included (identity is preserved byte-for-byte;
// a rebuild changes the CHECK, never the data). Both executed rebuilds (v5,
// v6) copy this same list: the v6 rebuild reads a v5-shaped table, and the
// new link_target_id column fills with its NULL default.
const tilesRebuildColumns = `id, object_id, version, grid_id, kind, x, y, w, h,
	view_x, view_y, view_zoom, child_grid_id,
	text_x, text_y, text_w, text_h, text_mode, blob_id,
	url_string, preview_blob_id, alt_text, alt_user, content_zoom, url_history,
	created_at, updated_at`

// rebuildTilesForPaneKind is the v5 rebuild (adds the 'pane' kind to the
// CHECK). Note the chain's convergence contract: a rebuild always creates
// tiles_new from the CURRENT tilesTableDDL text, so an old DB replaying v5
// lands directly on the latest shape and the later rebuild steps are
// idempotent re-runs — TestSchemaEquivalence proves the chain and a fresh
// Open converge either way.
func rebuildTilesForPaneKind(ctx context.Context, tx *sql.Tx) error {
	return rebuildTiles(ctx, tx)
}

// rebuildTilesForLinkTarget is the v6 rebuild (adds link_target_id and the
// CHECK's link branch).
func rebuildTilesForLinkTarget(ctx context.Context, tx *sql.Tx) error {
	return rebuildTiles(ctx, tx)
}

// rebuildTiles rebuilds the tiles table into the current shape: create
// tiles_new from the SAME DDL text a fresh Open uses (tilesTableDDL — one
// source, no drift), copy every row id-for-id, drop the old table, rename,
// recreate the indexes.
//
// The sqlite_sequence save/restore is load-bearing: DROP TABLE tiles deletes
// its sqlite_sequence row, and the copy re-seeds at the max SURVIVING id — so
// without the restore, the ids of tiles deleted above that max would be
// REUSED after the migration, violating the "ids are never reused" invariant
// (embeds, deep links, and client caches are keyed by id and would resolve to
// the wrong tile). The fixture in migration_harness_test.go pins this trap.
func rebuildTiles(ctx context.Context, tx *sql.Tx) error {
	var seq sql.NullInt64
	err := tx.QueryRowContext(ctx,
		`SELECT seq FROM sqlite_sequence WHERE name = 'tiles'`).Scan(&seq)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("read tiles sequence: %w", err)
	}
	for _, ddl := range []string{
		tilesTableDDL("tiles_new"),
		`INSERT INTO tiles_new (` + tilesRebuildColumns + `)
			SELECT ` + tilesRebuildColumns + ` FROM tiles`,
		`DROP TABLE tiles`,
		`ALTER TABLE tiles_new RENAME TO tiles`,
	} {
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("rebuild tiles: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, tilesIndexDDL); err != nil {
		return fmt.Errorf("rebuild tiles indexes: %w", err)
	}
	if seq.Valid {
		// The rename carried tiles_new's sequence row (seeded at max surviving
		// id) over to 'tiles'; raise it back to the pre-rebuild high-water mark
		// so deleted ids stay dead. INSERT covers the empty-table edge (no row
		// was minted by the copy).
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO sqlite_sequence (name, seq) SELECT 'tiles', 0
			 WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = 'tiles')`); err != nil {
			return fmt.Errorf("seed tiles sequence: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE sqlite_sequence SET seq = ? WHERE name = 'tiles' AND seq < ?`,
			seq.Int64, seq.Int64); err != nil {
			return fmt.Errorf("restore tiles sequence: %w", err)
		}
	}
	return nil
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
