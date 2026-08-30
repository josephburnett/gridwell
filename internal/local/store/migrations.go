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
// the file without reading a single row. See internal/local/store/CLAUDE.md.
const applicationID = 0x4757654C // "GWeL"

// schemaVersion is the schema generation this binary materializes. It is
// recorded in the SQLite header via PRAGMA user_version — the canonical,
// table-free place for an application schema version. Open stamps it on a
// fresh DB and refuses to open a DB stamped NEWER than this binary knows
// about (an older binary against a future-schema DB would misread rows).
//
// v1 is frozen; see tablesV1 in schema.go. Data written by any released
// binary stays readable, and the DB is never deleted to absorb a schema
// change. A change bumps schemaVersion by exactly one and appends one entry
// to migrations, plus one test fixture. The column descriptor in columns.go
// stays the latest shape, and TestSchemaEquivalence proves a fresh Open
// equals tablesV1 plus the full chain, which is what makes the fresh-DB
// stamp shortcut in applyMigrations sound. The full contract is
// internal/local/store/CLAUDE.md.
const schemaVersion = 12

// migration is one step that brings a DB from version to-1 up to version to.
// Additive — a column with a default, a table, an index — is the default and
// covers almost every change. A drop is possible but must be recorded in the
// chain entry's comment: only storage no released binary reads for any
// user-visible meaning may go, and the step must preserve every surviving row
// and the sqlite_sequence seeds. See internal/local/store/CLAUDE.md.
type migration struct {
	to  int
	run func(ctx context.Context, tx *sql.Tx) error
}

// migrations is the ordered list of post-v1 schema migrations; entry i brings
// a DB from version i+1 to i+2. See the schemaVersion doc above and
// internal/local/store/CLAUDE.md.
var migrations = []migration{
	// v2: alt_user marks alt_text as user-owned, so automatic captures never
	// overwrite a user-set name.
	{to: 2, run: addColumnDDL(`ALTER TABLE tiles ADD COLUMN alt_user INTEGER NOT NULL DEFAULT 0`)},
	// v3: content_zoom, the per-tile content scale. Framing.
	{to: 3, run: addColumnDDL(`ALTER TABLE tiles ADD COLUMN content_zoom REAL NOT NULL DEFAULT 0`)},
	// v4: url_history, a url tile's navigation back-stack across freeze and
	// revive.
	{to: 4, run: addColumnDDL(`ALTER TABLE tiles ADD COLUMN url_history TEXT`)},
	// v5: the 'pane' tile kind. A kind lives in the tiles table-level CHECK,
	// which ALTER TABLE cannot touch, so this is a table rebuild. No column
	// changes; only the CHECK.
	{to: 5, run: rebuildTilesForPaneKind},
	// v6: link_target_id, the leaf-link variant — a text, url, shell, or pane
	// tile whose content lives in another plugin's tile. The CHECK gains the
	// link branch, and a url link row has url_string NULL, which the v5 url
	// branch forbade, so this is a rebuild rather than an ADD COLUMN. Old
	// rows get link_target_id NULL, which is exactly their old meaning.
	{to: 6, run: rebuildTilesForLinkTarget},
	// v7: url_frozen, the user's standing freeze on a url tile. If-missing
	// because the v6 rebuild materializes the current template.
	{to: 7, run: addColumnIfMissingDDL("tiles", "url_frozen",
		`ALTER TABLE tiles ADD COLUMN url_frozen INTEGER NOT NULL DEFAULT 0`)},
	// v8: configure_plugin_id, marking a childless well. The well CHECK
	// branch gains the childless variant, so this is a rebuild. Old rows all
	// have child grids and copy through unchanged; the new column fills with
	// its '' default.
	{to: 8, run: rebuildTilesForConfigurePlugin},
	// v9: every plugin's memory joins the home tables — ns, key, and
	// tombstoned on tiles; ns, context_key, and a root viewport on grids;
	// the listings table; and the two partial unique indexes. Every column
	// carries a default, and home rows are the defaults. If-missing because
	// a rebuild at an earlier version materializes the current template,
	// columns included.
	{to: 9, run: migrateV9},
	// v10 retires three pieces of dead storage. None carried a user-visible
	// meaning any binary read:
	//
	//   - the `session` table: one Chromium session per DB. The session is
	//     host-local now and nothing reads or writes the table.
	//   - tiles.configure_plugin_id and the well CHECK's childless branch:
	//     nothing can mint a childless well and no reader remains. Rows that
	//     do exist are preserved as ordinary wells, each given a fresh empty
	//     child grid, so the user's tile stays where they put it and opens.
	//   - object_id on tiles and grids: a 128-bit provenance mint written on
	//     every row and copied through every clone and link, with no reader
	//     that decides anything on it.
	//
	// tiles is a rebuild, because the CHECK must change; grids is a DROP
	// COLUMN, with no CHECK involved. Both preserve every row and the
	// AUTOINCREMENT seeds.
	{to: 10, run: migrateV10},
	// v11 folds three framing representations into one. It was stored as a
	// well's integer window origin (tiles.view_x/view_y) plus an intrinsic
	// ratio, as home's root in the `system` KV table, and as a plugin
	// context's root on its grid row. Now it is one shape: a float center
	// plus the intrinsic zoom, on the doorway tile
	// (tiles.view_cx/view_cy/view_zoom) or, for a root with no doorway, on
	// the grid row (grids.root_cx/cy/zoom), home's included, in the empty
	// namespace.
	//
	// Nothing user-visible changes: every stored origin becomes the center
	// the client already derived from it, origin + footprint/2, and a root's
	// synthetic doorway is 1x1, so + 0.5. view_x and view_y retire, because
	// the center they fed is stored directly. tiles is a rebuild, since a
	// column pair retires and the values convert; grids and system are
	// in-place updates.
	{to: 11, run: migrateV11},
	// v12 retires the `listings` table. It held one blob per plugin context,
	// the last ListResponse the plugin gave, so a dark source could be served
	// the remembered listing. That is cache, and cache lives in cache.db
	// (internal/sourcecache), one engine for plugins and connections alike.
	//
	// Nothing user-visible is lost, which is why this is a plain drop and not
	// a conversion: every fact a listing row carried that the user can see
	// was already a durable row beside it — the ids the node minted, the
	// placement the user made, the labels, the child grids — and the rest
	// re-warms on the next successful List. The table stands alone, with no
	// CHECK, no AUTOINCREMENT seed, and nothing referencing it, so tiles and
	// grids are not touched and no sequence is disturbed.
	{to: 12, run: migrateV12},
}

// migrateV12 drops the retired `listings` table. IfExists because a file may
// arrive without it: v9's literal CREATE ran only on files that passed
// through v9, and a fresh Open no longer materializes it.
func migrateV12(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS listings`)
	return err
}

// rebuildTilesForPaneKind is the v5 rebuild, adding the 'pane' kind to the
// CHECK. The chain's convergence contract lives here: a rebuild always
// creates tiles_new from the current tilesTableDDL text, so an old DB
// replaying v5 lands directly on the latest shape and the later rebuild
// steps are idempotent re-runs. TestSchemaEquivalence proves the chain and a
// fresh Open converge either way.
func rebuildTilesForPaneKind(ctx context.Context, tx *sql.Tx) error {
	return rebuildTiles(ctx, tx, 4)
}

// rebuildTilesForLinkTarget is the v6 rebuild, adding link_target_id and the
// CHECK's link branch.
func rebuildTilesForLinkTarget(ctx context.Context, tx *sql.Tx) error {
	return rebuildTiles(ctx, tx, 5)
}

// rebuildTilesForConfigurePlugin is the v8 rebuild, adding
// configure_plugin_id and the well CHECK's childless variant. It reads a
// v7-shaped table, so it copies the v7 column list.
func rebuildTilesForConfigurePlugin(ctx context.Context, tx *sql.Tx) error {
	return rebuildTiles(ctx, tx, 7)
}

// rebuildTiles rebuilds the tiles table into the current shape: create
// tiles_new from the same DDL text a fresh Open uses (tilesTableDDL, one
// source, no drift), copy every row id-for-id, drop the old table, rename,
// and recreate the indexes.
//
// It recreates only tilesIndexDDL's indexes. A rebuild running at or after v9
// must also recreate externalsIndexDDL's idx_tiles_live_key, which DROP TABLE
// takes with it.
//
// The sqlite_sequence save and restore is load-bearing: DROP TABLE tiles
// deletes its sqlite_sequence row, and the copy re-seeds at the maximum
// surviving id, so without the restore the ids of tiles deleted above that
// maximum would be reused, violating "ids are never reused". Deep links and
// client caches are keyed by id and would resolve to the wrong tile. The
// fixture in migration_harness_test.go pins this trap.
func rebuildTiles(ctx context.Context, tx *sql.Tx, reads int) error {
	columns := rebuildColumns(reads)
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
		// The rename carried tiles_new's sequence row, seeded at the maximum
		// surviving id, over to 'tiles'. Raise it back to the pre-rebuild
		// high-water mark so deleted ids stay dead. The INSERT covers the
		// empty-table edge, where the copy minted no row.
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

// rebuildSelect maps a rebuild's destination column list onto the SELECT
// expressions that read those columns out of the table as it stands now.
// Every column reads as itself but one pair: on a pre-v11 source the framing
// is an integer window origin (view_x, view_y), and the center the v11 shape
// stores is reconstructed with the same arithmetic the client uses to display
// it, origin + footprint/2. A well therefore shows exactly the framing it
// showed before, whichever rebuild step an old file converts through.
func rebuildSelect(ctx context.Context, tx *sql.Tx, columns string) (string, error) {
	has, err := hasColumn(ctx, tx, "tiles", "view_cx")
	if err != nil || has {
		return columns, err
	}
	return strings.Replace(columns, "view_cx, view_cy",
		"view_x + w / 2.0, view_y + h / 2.0", 1), nil
}

// migrateV11 folds the three framing representations into one; see the chain
// entry. Order matters:
//
//  1. A plugin context's root (grids.root_cx/cy, a non-empty ns) was written
//     as the origin of a 1x1 synthetic doorway and read back as origin + 1/2,
//     so it converts by + 0.5. This runs first, while home's root row is
//     still empty; otherwise step 2's already-converted value would be
//     shifted a second time.
//  2. Home's root moves out of the `system` KV table onto its root grid row,
//     in the empty namespace, converted the same way, and the three keys are
//     deleted. A never-visited home, with zoom 0, copies nothing and stays
//     never visited in the one convention the new shape has.
//  3. tiles rebuilds, converting view_x and view_y into view_cx and view_cy
//     through rebuildSelect, and dropping the retired pair.
func migrateV11(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE grids SET root_cx = root_cx + 0.5, root_cy = root_cy + 0.5
		 WHERE ns != '' AND root_zoom IS NOT NULL`); err != nil {
		return fmt.Errorf("convert context root framing: %w", err)
	}
	if err := moveHomeRootFraming(ctx, tx); err != nil {
		return err
	}
	if err := rebuildTiles(ctx, tx, 9); err != nil {
		return err
	}
	// DROP TABLE tiles took idx_tiles_live_key with it; see rebuildTiles.
	_, err := tx.ExecContext(ctx, externalsIndexDDL)
	return err
}

// moveHomeRootFraming copies home's root viewport from the system KV table
// onto its root grid row and deletes the keys. The stored cx and cy were the
// origin of the 1x1 synthetic doorway the client framed a root through, so
// they convert by + 0.5, giving the same center the client showed. A missing
// or zero zoom means never visited, so there is nothing to carry.
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
// statement.
func addColumnDDL(ddl string) func(ctx context.Context, tx *sql.Tx) error {
	return func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, ddl)
		return err
	}
}

// addColumnIfMissingDDL is addColumnDDL for a column added after a
// table-rebuild migration. A rebuild materializes the current tilesTableDDL,
// so a chain that passes through it already carries every later column and
// the plain ALTER would fail with "duplicate column"; an older file whose
// rebuild ran under an older binary still needs it. Both paths converge on
// the same shape, which TestSchemaEquivalence proves.

// migrateV9 adds the plugin-memory columns and tables; see the chain entry.
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
	// The listings table is spelled here because this step must produce v9's
	// shape whatever a later template says. v12 retires it, so the chain
	// creates it and drops it again, and the two routes still converge; see
	// TestSchemaEquivalence. The two partial indexes name the columns just
	// added, so they ride here too, and in Open's post-migration step for a
	// fresh file, which never runs the chain: externalsIndexDDL, one text.
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

// migrateV10 retires the dead storage listed in the chain entry above: the
// `session` table, tiles.configure_plugin_id and the well CHECK's childless
// branch, and object_id on both record tables.
//
// Order matters. grids.object_id goes first, by ALTER TABLE DROP COLUMN;
// SQLite refuses while an index names the column, so that index goes first.
// grids is never dropped, so its AUTOINCREMENT seed is untouched. Then the
// stale childless wells are adopted — each gets a fresh empty child grid,
// minted through the now-current grids shape — because the rebuilt tiles
// CHECK no longer admits a well without one, and dropping the user's tile
// would break the rule that things stay as you left them. tiles goes last,
// through the shared rebuild, which saves and restores its seed.
func migrateV10(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS session`); err != nil {
		return fmt.Errorf("drop session table: %w", err)
	}
	// The column exists on every file that reaches v10 through the chain, but
	// a file whose earlier rebuild already materialized the current template
	// has neither the index nor the column: the same convergence case
	// addColumnIfMissingDDL covers on the additive side.
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
	// tilesIndexDDL does not create idx_tiles_object_id, but an old file
	// still carries it. Drop it explicitly so the migrated and fresh index
	// sets match exactly.
	if _, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS idx_tiles_object_id`); err != nil {
		return fmt.Errorf("drop idx_tiles_object_id: %w", err)
	}
	if err := rebuildTiles(ctx, tx, 10); err != nil {
		return err
	}
	// DROP TABLE tiles took idx_tiles_live_key with it. That index is created
	// by externalsIndexDDL, not tilesIndexDDL, so the shared rebuild cannot
	// know about it. Recreate it here; both statements are IF NOT EXISTS, so
	// the grids half is a no-op. Any rebuild after v9 must do the same.
	_, err = tx.ExecContext(ctx, externalsIndexDDL)
	return err
}

// adoptStalePluginWells turns every childless well carrying
// configure_plugin_id into an ordinary interior well by minting it a fresh
// empty child grid. Nothing can fill such a well any more, so the alternative
// to adoption is deleting a tile the user placed. After this the well CHECK's
// childless branch has nothing left to admit.
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

// hasColumn reports whether table already has the named column: the one
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
// the pending entries of migs. The engine itself — the fresh stamp, the
// foreign-file and newer-version refusals, the chain — lives in
// internal/dbformat.EnsureVersion, one implementation of the format contract.
// The fresh-DB stamp shortcut is sound here because a fresh Open materializes
// tablesDDL(), and TestSchemaEquivalence proves that shape equals tablesV1
// plus the full chain.
//
// migs and target are parameters rather than the globals so the engine can be
// exercised by tests with a synthetic chain against a frozen-v1 DB.
func (s *Store) migrateUp(ctx context.Context, migs []migration, target int) error {
	chain := make([]dbformat.Migration, 0, len(migs))
	for _, m := range migs {
		chain = append(chain, dbformat.Migration{To: m.to, Run: m.run})
	}
	return dbformat.EnsureVersion(ctx, s.db, applicationID, target, chain)
}
