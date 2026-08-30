package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

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
const schemaVersion = 11

// migration is one step that brings a DB from version to-1 up to version
// to. Additive (a column with a default, a table, an index) is the
// DEFAULT and covers almost every change. A DROP is possible but is an
// OWNER DECISION recorded in the chain entry's comment: only storage no
// released binary reads for any user-visible meaning may go, and the step
// must preserve every surviving row and the sqlite_sequence seeds (see
// internal/local/store/CLAUDE.md).
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
	// v7: url_frozen — the user's standing freeze on a url tile (issue
	// #237): descent does not auto-go-live until reconnect clears it.
	// If-missing because the v6 REBUILD materializes the current template.
	{to: 7, run: addColumnIfMissingDDL("tiles", "url_frozen",
		`ALTER TABLE tiles ADD COLUMN url_frozen INTEGER NOT NULL DEFAULT 0`)},
	// v8: configure_plugin_id — the unconfigured plugin well (issue #251):
	// a childless well waiting for a parameterized plugin's instance. The
	// well CHECK branch gains the childless variant, so this is a rebuild.
	// Old rows all have child grids and copy through unchanged; the new
	// column fills with its '' default.
	{to: 8, run: rebuildTilesForConfigurePlugin},
	// v9 (2026-08-29, docs/one-node.md §2.6): the externals' memory joins
	// the home tables — ns/key/tombstoned on tiles, ns/context_key and a
	// root viewport on grids, the listings table, and the two partial
	// unique indexes. Every column carries a default; home rows are the
	// defaults. (IfMissing: a rebuild at an earlier version materializes
	// the current template, columns included.)
	{to: 9, run: migrateV9},
	// v10 (2026-08-29): DEAD STORAGE RETIRED. Owner decision, recorded
	// here per docs/simplify-plan.md — the forever promise is that data
	// written by a released binary stays READABLE, and none of these
	// three carried a user-visible meaning any binary read:
	//
	//   - the `session` table: one Chromium session per DB, moved by
	//     GetSession/PutSession. Both RPCs died 2026-07-26 (the session
	//     became host-local); nothing has read or written the table
	//     since, so every row that could exist is stale bytes.
	//   - tiles.configure_plugin_id and the well CHECK's childless
	//     branch: the unconfigured plugin well (#251). The instance
	//     picker retired 2026-08-23 and the create/adopt verbs went
	//     2026-08-29, so no row can be minted and no reader remains.
	//     Rows that DO exist (minted 2026-08-08..23) are preserved as
	//     ORDINARY wells — each is given a fresh empty child grid, so
	//     the user's tile stays where they put it and now opens.
	//   - object_id on tiles and grids: a 128-bit provenance mint
	//     written on every row and copied through every clone and link,
	//     with no reader anywhere that decides anything on it (verified
	//     across store, clone, link, conv, client and tests — the one
	//     place lineage COULD have been read, dragdrop.HiddenMatch,
	//     deliberately matches row id and has a test saying so).
	//
	// Shape: tiles is a REBUILD (the CHECK must change), grids is a
	// DROP COLUMN (no CHECK involved). Both preserve every row and the
	// AUTOINCREMENT seeds.
	{to: 10, run: migrateV10},
	// v11 (2026-08-29, docs/simplify-plan.md S4): ONE framing shape.
	// "How this grid looked when I left it through this doorway" was
	// stored three ways — a well's integer window ORIGIN
	// (tiles.view_x/view_y) plus an intrinsic ratio, home's root as a
	// float center in the `system` KV table (root_view_cx/cy/root_zoom),
	// and a plugin context's root as a float center on its grid row. Now
	// it is one: a float CENTER plus that same intrinsic zoom, on the
	// DOORWAY tile (tiles.view_cx/view_cy/view_zoom) or, for a root with
	// no doorway, on the GRID row (grids.root_cx/cy/zoom) — home's
	// included, at ns = ''.
	//
	// Nothing user-visible changes: every stored origin becomes the
	// center the client already derived from it to display the grid
	// (origin + footprint/2; the root's synthetic doorway is 1×1, so
	// + 0.5). view_x/view_y RETIRE — after this step nothing reads them
	// for any meaning, since the center they fed is now stored directly.
	// Shape: tiles is a REBUILD (a column pair retires and the values
	// convert); grids and system are in-place updates.
	{to: 11, run: migrateV11},
}

// tilesRebuildColumns is the explicit column list a rebuild copies — every
// tiles column as of v5 that still exists, id included (identity is
// preserved byte-for-byte; a rebuild changes the CHECK, never the data).
// The v5 and v6 rebuilds copy this same list: the v6 rebuild reads a
// v5-shaped table, and the new link_target_id column fills with its NULL
// default.
//
// object_id is NOT here: it was retired at v10, and a rebuild always
// materializes the CURRENT tilesTableDDL, so a v4 file replaying v5 lands
// on the v11 shape and simply does not carry the column forward. That is
// the convergence contract working as designed — the fresh and migrated
// routes must end at the same schema.
//
// view_cx/view_cy are the v11 framing columns, named here as DESTINATION
// columns: rebuildSelect reads them from a pre-v11 source as
// view_x + w/2 and view_y + h/2, so an old file replaying ANY rebuild
// converts its framing exactly once and keeps the picture it had.
const tilesRebuildColumns = `id, version, grid_id, kind, x, y, w, h,
	view_cx, view_cy, view_zoom, child_grid_id,
	text_x, text_y, text_w, text_h, text_mode, blob_id,
	url_string, preview_blob_id, alt_text, alt_user, content_zoom, url_history,
	created_at, updated_at`

// tilesRebuildColumnsV8 is the copy list for the v8 rebuild, which reads a
// v7-shaped table — so it must ALSO carry the post-v5 columns
// (link_target_id, url_frozen) or every link row and standing freeze would
// be silently reset to defaults. A rebuild's copy list is always "every
// column of the version it reads", never the shared v5 list.
const tilesRebuildColumnsV8 = tilesRebuildColumns + `,
	link_target_id, url_frozen`

// rebuildTilesForPaneKind is the v5 rebuild (adds the 'pane' kind to the
// CHECK). Note the chain's convergence contract: a rebuild always creates
// tiles_new from the CURRENT tilesTableDDL text, so an old DB replaying v5
// lands directly on the latest shape and the later rebuild steps are
// idempotent re-runs — TestSchemaEquivalence proves the chain and a fresh
// Open converge either way.
func rebuildTilesForPaneKind(ctx context.Context, tx *sql.Tx) error {
	return rebuildTiles(ctx, tx, tilesRebuildColumns)
}

// rebuildTilesForLinkTarget is the v6 rebuild (adds link_target_id and the
// CHECK's link branch).
func rebuildTilesForLinkTarget(ctx context.Context, tx *sql.Tx) error {
	return rebuildTiles(ctx, tx, tilesRebuildColumns)
}

// rebuildTilesForConfigurePlugin is the v8 rebuild (adds
// configure_plugin_id and the well CHECK's childless variant — issue #251).
// It reads a v7-shaped table, so it copies the v7 column list.
func rebuildTilesForConfigurePlugin(ctx context.Context, tx *sql.Tx) error {
	return rebuildTiles(ctx, tx, tilesRebuildColumnsV8)
}

// rebuildTiles rebuilds the tiles table into the current shape: create
// tiles_new from the SAME DDL text a fresh Open uses (tilesTableDDL — one
// source, no drift), copy every row id-for-id, drop the old table, rename,
// recreate the indexes.
//
// Caution: it recreates only tilesIndexDDL's indexes. A rebuild running at
// or after v9 must ALSO recreate externalsIndexDDL's idx_tiles_live_key,
// which DROP TABLE takes with it (migrateV10 does).
//
// The sqlite_sequence save/restore is load-bearing: DROP TABLE tiles deletes
// its sqlite_sequence row, and the copy re-seeds at the max SURVIVING id — so
// without the restore, the ids of tiles deleted above that max would be
// REUSED after the migration, violating the "ids are never reused" invariant
// (embeds, deep links, and client caches are keyed by id and would resolve to
// the wrong tile). The fixture in migration_harness_test.go pins this trap.
func rebuildTiles(ctx context.Context, tx *sql.Tx, columns string) error {
	src, err := rebuildSelect(ctx, tx, columns)
	if err != nil {
		return err
	}
	var seq sql.NullInt64
	err = tx.QueryRowContext(ctx,
		`SELECT seq FROM sqlite_sequence WHERE name = 'tiles'`).Scan(&seq)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("read tiles sequence: %w", err)
	}
	for _, ddl := range []string{
		tilesTableDDL("tiles_new"),
		`INSERT INTO tiles_new (` + columns + `)
			SELECT ` + src + ` FROM tiles`,
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

// rebuildSelect maps a rebuild's DESTINATION column list onto the SELECT
// expressions that read those columns out of the table as it stands now.
// Every column reads as itself but one pair: on a PRE-v11 source the
// framing is an integer window ORIGIN (view_x/view_y), and the center the
// v11 shape stores is reconstructed with the same arithmetic the client
// always used to display it — origin + footprint/2. So a well shows
// exactly the framing it showed before, whichever rebuild step an old
// file happens to convert through.
func rebuildSelect(ctx context.Context, tx *sql.Tx, columns string) (string, error) {
	has, err := hasColumn(ctx, tx, "tiles", "view_cx")
	if err != nil || has {
		return columns, err
	}
	return strings.Replace(columns, "view_cx, view_cy",
		"view_x + w / 2.0, view_y + h / 2.0", 1), nil
}

// migrateV11 folds the three framing representations into one (see the
// chain entry). Order matters:
//
//  1. A plugin context's root (grids.root_cx/cy, ns != ”) was written as
//     the ORIGIN of a 1×1 synthetic doorway and read back as origin + 1/2,
//     so it converts by + 0.5. This runs FIRST, while home's root row is
//     still empty — otherwise step 2's already-converted value would be
//     shifted a second time.
//  2. Home's root moves out of the `system` KV table onto its root GRID
//     row (ns = ”), converted the same way; the three keys are deleted.
//     A never-visited home (zoom 0, the bootstrap seed) copies nothing —
//     it stays "never visited" in the one convention the new shape has.
//  3. tiles rebuilds, converting view_x/view_y into view_cx/view_cy
//     (rebuildSelect) and dropping the retired pair.
func migrateV11(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE grids SET root_cx = root_cx + 0.5, root_cy = root_cy + 0.5
		 WHERE ns != '' AND root_zoom IS NOT NULL`); err != nil {
		return fmt.Errorf("convert context root framing: %w", err)
	}
	if err := moveHomeRootFraming(ctx, tx); err != nil {
		return err
	}
	if err := rebuildTiles(ctx, tx, tilesRebuildColumnsV10); err != nil {
		return err
	}
	// DROP TABLE tiles took idx_tiles_live_key with it (see rebuildTiles).
	_, err := tx.ExecContext(ctx, externalsIndexDDL)
	return err
}

// moveHomeRootFraming copies home's root viewport from the frozen system
// KV table onto its root grid row and deletes the keys. The stored cx/cy
// were the ORIGIN of the 1×1 synthetic doorway the client framed a root
// through, so they convert by + 0.5 — the same center the client showed.
// A missing or zero zoom means never visited: nothing to carry.
func moveHomeRootFraming(ctx context.Context, tx *sql.Tx) error {
	read := func(key string) (float64, error) {
		var v sql.NullFloat64
		err := tx.QueryRowContext(ctx, `SELECT CAST(value AS REAL) FROM system WHERE key = ?`, key).Scan(&v)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return v.Float64, err
	}
	zoom, err := read("root_zoom")
	if err != nil {
		return fmt.Errorf("read home root zoom: %w", err)
	}
	if zoom > 0 {
		cx, err := read("root_view_cx")
		if err != nil {
			return fmt.Errorf("read home root cx: %w", err)
		}
		cy, err := read("root_view_cy")
		if err != nil {
			return fmt.Errorf("read home root cy: %w", err)
		}
		var rootID sql.NullString
		if err := tx.QueryRowContext(ctx,
			`SELECT value FROM system WHERE key = ?`, systemKeyRootGridID).Scan(&rootID); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read home root grid id: %w", err)
		}
		if rootID.Valid {
			if _, err := tx.ExecContext(ctx,
				`UPDATE grids SET root_cx = ?, root_cy = ?, root_zoom = ? WHERE id = ? AND ns = ''`,
				cx+0.5, cy+0.5, zoom, rootID.String); err != nil {
				return fmt.Errorf("write home root framing: %w", err)
			}
		}
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM system WHERE key IN ('root_view_cx', 'root_view_cy', 'root_zoom')`); err != nil {
		return fmt.Errorf("retire the home root framing keys: %w", err)
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

// addColumnIfMissingDDL is addColumnDDL for a column added AFTER a
// table-rebuild migration. A rebuild materializes the CURRENT
// tablesTemplate (one DDL source, no drift — the v5/v6 recipe), so a chain
// that passes through it already carries every later column and the plain
// ALTER would fail with "duplicate column"; a genuinely old file whose
// rebuild ran under an older binary still needs it. Both paths converge on
// the same shape — TestSchemaEquivalence proves it.
// migrateV9 adds the externals' columns and tables (see the chain entry).
func migrateV9(ctx context.Context, tx *sql.Tx) error {
	steps := []func(context.Context, *sql.Tx) error{
		addColumnIfMissingDDL("grids", "ns", `ALTER TABLE grids ADD COLUMN ns TEXT NOT NULL DEFAULT ''`),
		addColumnIfMissingDDL("grids", "context_key", `ALTER TABLE grids ADD COLUMN context_key TEXT NOT NULL DEFAULT ''`),
		addColumnIfMissingDDL("grids", "root_cx", `ALTER TABLE grids ADD COLUMN root_cx REAL`),
		addColumnIfMissingDDL("grids", "root_cy", `ALTER TABLE grids ADD COLUMN root_cy REAL`),
		addColumnIfMissingDDL("grids", "root_zoom", `ALTER TABLE grids ADD COLUMN root_zoom REAL`),
		addColumnIfMissingDDL("tiles", "ns", `ALTER TABLE tiles ADD COLUMN ns TEXT NOT NULL DEFAULT ''`),
		addColumnIfMissingDDL("tiles", "key", `ALTER TABLE tiles ADD COLUMN key TEXT NOT NULL DEFAULT ''`),
		addColumnIfMissingDDL("tiles", "tombstoned", `ALTER TABLE tiles ADD COLUMN tombstoned INTEGER NOT NULL DEFAULT 0`),
	}
	for _, step := range steps {
		if err := step(ctx, tx); err != nil {
			return err
		}
	}
	// The listings table is CREATE IF NOT EXISTS in tablesDDL (it appears
	// at Open); the two partial indexes name the columns just added, so
	// they ride here (and in Open's post-migration step for a fresh
	// file, which never runs the chain — externalsIndexDDL, one text).
	if _, err := tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS listings (
    grid_id       INTEGER PRIMARY KEY REFERENCES grids(id),
    entries       BLOB NOT NULL,
    authoritative INTEGER NOT NULL DEFAULT 0
);`); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, externalsIndexDDL)
	return err
}

// migrateV10 retires the dead storage listed in the chain entry above:
// the `session` table, tiles.configure_plugin_id (and the well CHECK's
// childless branch), and object_id on both record tables.
//
// Order matters. grids.object_id goes FIRST, by ALTER TABLE DROP COLUMN
// (SQLite refuses while an index names the column, so its index goes
// first); grids is never DROPped, so its AUTOINCREMENT seed is untouched.
// Then the stale childless wells are ADOPTED — each gets a fresh empty
// child grid, minted through the now-current grids shape — because the
// rebuilt tiles CHECK no longer admits a well without one, and dropping
// the user's tile instead of keeping it would break the guiding rule
// (things stay as you left them). tiles goes last, through the shared
// rebuild, which saves and restores its seed.
func migrateV10(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS session`); err != nil {
		return fmt.Errorf("drop session table: %w", err)
	}
	// The column exists on every file that reaches v10 through the chain,
	// but a file whose earlier REBUILD already materialized the current
	// (v10) template has neither the index nor the column — the same
	// convergence case addColumnIfMissingDDL covers on the additive side.
	has, err := hasColumn(ctx, tx, "grids", "object_id")
	if err != nil {
		return err
	}
	if has {
		for _, ddl := range []string{
			`DROP INDEX IF EXISTS idx_grids_object_id`,
			`ALTER TABLE grids DROP COLUMN object_id`,
		} {
			if _, err := tx.ExecContext(ctx, ddl); err != nil {
				return fmt.Errorf("drop grids.object_id: %w", err)
			}
		}
	}
	if err := adoptStalePluginWells(ctx, tx); err != nil {
		return err
	}
	// idx_tiles_object_id is not recreated by tilesIndexDDL any more, but
	// an old file still carries it and DROP TABLE tiles would leave it
	// behind only if it survived — drop it explicitly so the migrated and
	// fresh index sets match exactly.
	if _, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS idx_tiles_object_id`); err != nil {
		return fmt.Errorf("drop idx_tiles_object_id: %w", err)
	}
	if err := rebuildTiles(ctx, tx, tilesRebuildColumnsV10); err != nil {
		return err
	}
	// DROP TABLE tiles took idx_tiles_live_key with it — that index is
	// created by externalsIndexDDL (v9), not tilesIndexDDL, so the shared
	// rebuild cannot know about it. Recreate it here; both statements are
	// IF NOT EXISTS, so the grids half is a no-op. (Any FUTURE rebuild
	// after v9 must do the same — migrateV11 does.)
	_, err = tx.ExecContext(ctx, externalsIndexDDL)
	return err
}

// tilesRebuildColumnsV10 is the copy list for the v10 rebuild: every tiles
// column of the version it READS (v9) that SURVIVES v10 — so the post-v5
// columns and the v9 externals' columns ride across, and object_id +
// configure_plugin_id are simply not carried. A rebuild's copy list is
// always "every surviving column of the version it reads".
const tilesRebuildColumnsV10 = tilesRebuildColumnsV8 + `,
	ns, key, tombstoned`

// adoptStalePluginWells turns every UNCONFIGURED PLUGIN WELL (#251: a
// childless well carrying configure_plugin_id) into an ordinary interior
// well by minting it a fresh empty child grid. Those wells could only be
// created between 2026-08-08 and 2026-08-23; the picker that would have
// filled them is gone, so the alternative to adoption is deleting a tile
// the user placed — which the guiding rule forbids. After this the well
// CHECK's childless branch has nothing left to admit.
func adoptStalePluginWells(ctx context.Context, tx *sql.Tx) error {
	has, err := hasColumn(ctx, tx, "tiles", "configure_plugin_id")
	if err != nil || !has {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, created_at FROM tiles
		WHERE kind = 'well' AND child_grid_id IS NULL AND configure_plugin_id != ''`)
	if err != nil {
		return fmt.Errorf("find stale plugin wells: %w", err)
	}
	type stale struct{ id, createdAt int64 }
	var wells []stale
	for rows.Next() {
		var w stale
		if err := rows.Scan(&w.id, &w.createdAt); err != nil {
			rows.Close()
			return fmt.Errorf("scan stale plugin well: %w", err)
		}
		wells = append(wells, w)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, w := range wells {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO grids (created_at, updated_at) VALUES (?, ?)`, w.createdAt, w.createdAt)
		if err != nil {
			return fmt.Errorf("mint child grid for stale plugin well %d: %w", w.id, err)
		}
		gid, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE tiles SET child_grid_id = ? WHERE id = ?`, gid, w.id); err != nil {
			return fmt.Errorf("adopt stale plugin well %d: %w", w.id, err)
		}
	}
	return nil
}

// hasColumn reports whether table already has the named column — the one
// PRAGMA table_info read the migration steps share.
func hasColumn(ctx context.Context, tx *sql.Tx, table, column string) (bool, error) {
	rows, err := tx.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid       int
			name, typ string
			notnull   int
			dflt      sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func addColumnIfMissingDDL(table, column, ddl string) func(ctx context.Context, tx *sql.Tx) error {
	return func(ctx context.Context, tx *sql.Tx) error {
		has, err := hasColumn(ctx, tx, table, column)
		if err != nil || has {
			return err // has: the rebuild already materialized it
		}
		_, err = tx.ExecContext(ctx, ddl)
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
