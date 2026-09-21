package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/josephburnett/gridwell/internal/dbformat"
)

// applicationID marks a database file as a Gridwell DB, written into the
// SQLite header by PRAGMA application_id ("GWeL"), so file(1) and the open
// path recognize the file without reading a row.
const applicationID = 0x4757654C // "GWeL"

// schemaVersion is the schema generation this binary materializes, recorded
// in PRAGMA user_version. Open stamps it on a fresh DB and refuses one
// stamped newer, which an older binary would misread. v1 is frozen (tablesV1
// in schema.go); a change bumps this by one and appends one entry to
// migrations plus one test fixture. TestSchemaEquivalence proves a fresh Open
// equals tablesV1 plus the full chain, which is what makes the fresh-DB stamp
// shortcut sound. The contract is CLAUDE.md in this directory.
const schemaVersion = 13

// migration is one step from version to-1 up to version to. Additive is the
// default. A drop must be recorded in the chain entry's comment: only storage
// no released binary reads for a user-visible meaning may go, and the step
// preserves every surviving row and the sqlite_sequence seeds.
type migration struct {
	to  int
	run func(ctx context.Context, tx *sql.Tx) error
}

// migrations is ordered; entry i brings a DB from version i+1 to i+2.
var migrations = []migration{
	// v2: alt_user marks alt_text as user-owned, so a capture never
	// overwrites a user-set name.
	{to: 2, run: addColumnDDL(`ALTER TABLE tiles ADD COLUMN alt_user INTEGER NOT NULL DEFAULT 0`)},
	// v3: content_zoom, the per-tile content scale. Framing.
	{to: 3, run: addColumnDDL(`ALTER TABLE tiles ADD COLUMN content_zoom REAL NOT NULL DEFAULT 0`)},
	// v4: url_history, a url tile's back-stack across freeze and revive.
	{to: 4, run: addColumnDDL(`ALTER TABLE tiles ADD COLUMN url_history TEXT`)},
	// v5: the 'pane' tile kind. A kind lives in the tiles table-level CHECK,
	// which ALTER TABLE cannot touch, so this is a table rebuild.
	{to: 5, run: rebuildTilesReading(4)},
	// v6: link_target_id, the leaf-link variant. The CHECK gains the link
	// branch, and a url link row has url_string NULL, which the v5 branch
	// forbade, so this is a rebuild. Old rows get NULL, their old meaning.
	{to: 6, run: rebuildTilesReading(5)},
	// v7: url_frozen, the user's standing freeze. If-missing
	// because the v6 rebuild materializes the current template.
	{to: 7, run: addColumnIfMissingDDL("tiles", "url_frozen",
		`ALTER TABLE tiles ADD COLUMN url_frozen INTEGER NOT NULL DEFAULT 0`)},
	// v8: configure_plugin_id, marking a childless well. The well CHECK gains
	// that variant, so this is a rebuild; old rows copy through unchanged.
	{to: 8, run: rebuildTilesReading(7)},
	// v9: every plugin's memory joins the home tables — ns, key and
	// tombstoned on tiles; ns, context_key and a root viewport on grids; the
	// listings table; two partial unique indexes. Every column carries a
	// default and home rows are the defaults.
	{to: 9, run: migrateV9},
	// v10 retires three pieces of dead storage, none carrying a user-visible
	// meaning any binary read: the `session` table, now that the Chromium
	// session is host-local; tiles.configure_plugin_id and the well CHECK's
	// childless branch, which nothing can mint any more, with existing rows
	// preserved as ordinary wells each given a fresh empty child grid; and
	// object_id on tiles and grids, a provenance mint no reader decided on.
	// tiles is a rebuild, because the CHECK must change; grids is a DROP
	// COLUMN. Both preserve every row and the AUTOINCREMENT seeds.
	{to: 10, run: migrateV10},
	// v11 folds three framing representations into one: a well's integer
	// window origin (tiles.view_x/view_y) plus an intrinsic ratio, home's
	// root in the `system` KV table, and a plugin context's root on its grid
	// row. The one shape is a float center plus the intrinsic zoom, on the
	// doorway tile (tiles.view_cx/cy/zoom) or, for a root with no doorway, on
	// the grid row (grids.root_cx/cy/zoom). Nothing user-visible changes:
	// every origin becomes the center the client already derived from it,
	// origin + footprint/2, and a root's synthetic doorway is 1x1, so + 0.5.
	// view_x and view_y retire. tiles is a rebuild; grids and system are
	// updated in place.
	{to: 11, run: migrateV11},
	// v12 retires the `listings` table, one blob per plugin context holding
	// the last ListResponse so a dark source could be served the remembered
	// listing. That is cache, and cache lives in cache.db. A plain drop, not
	// a conversion: every user-visible fact a listing row carried was already
	// a durable row beside it, and the rest re-warms on the next List. The
	// table stands alone, so no CHECK, seed or other table is disturbed.
	{to: 12, run: migrateV12},
	// v13 adopts the `connections` table into the chain. internal/connection
	// created it with a CREATE TABLE IF NOT EXISTS of its own, beside the
	// chain rather than in it, so the first change to its shape would have
	// had no migration lane and no fixture. The shape is the store's from
	// here; internal/connection keeps the queries. Adoption, not conversion:
	// the descriptor renders exactly the columns the ad-hoc statement wrote,
	// so an existing table is taken as it stands and every row keeps its
	// meaning. A home that never dialled anything gets one.
	{to: 13, run: migrateV13},
}

// migrateV12 drops the retired `listings` table. IfExists because v9's
// literal CREATE ran only on files that passed through v9.
func migrateV12(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS listings`)
	return err
}

// migrateV13 adopts or creates the connections table; see the chain entry. It
// renders connectionsTableDDL rather than a literal, because that identity
// with a fresh Open is what makes adopting an existing table legal.
func migrateV13(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, connectionsTableDDL())
	return err
}

// rebuildTilesReading is a chain entry that rebuilds tiles from a table of
// version n's shape. The chain's convergence contract lives here: a rebuild
// creates tiles_new from the current tilesTableDDL, so an old DB replaying an
// early rebuild lands on the latest shape and every later rebuild is an
// idempotent re-run.
func rebuildTilesReading(n int) func(ctx context.Context, tx *sql.Tx) error {
	return func(ctx context.Context, tx *sql.Tx) error {
		return rebuildTiles(ctx, tx, n)
	}
}

// rebuildTiles rebuilds the tiles table into the current shape: create
// tiles_new from the same DDL a fresh Open uses, copy every row id-for-id,
// drop, rename, recreate the indexes. It recreates only tilesIndexDDL's; a
// rebuild at or after v9 must also recreate externalsIndexDDL's
// idx_tiles_live_key, which DROP TABLE takes with it.
//
// The sqlite_sequence save and restore is load-bearing: DROP TABLE deletes
// the sequence row and the copy re-seeds at the maximum surviving id, so
// without the restore the ids of tiles deleted above that maximum would be
// reused and deep links would resolve to the wrong tile.
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
		// Raise the sequence back to the pre-rebuild high-water mark so
		// deleted ids stay dead. The INSERT covers the empty-table edge,
		// where the copy minted no row.
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

// rebuildSelect maps a rebuild's destination columns onto the expressions
// that read them out of the table as it stands. Every column reads as itself
// but one pair: on a pre-v11 source the framing is an integer window origin,
// and the center v11 stores is origin + footprint/2, the same arithmetic the
// client used to display it, so a well shows the framing it showed before.
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
//  1. A plugin context's root (grids.root_cx/cy, non-empty ns) converts by
//     + 0.5. First, while home's root row is still empty; otherwise step 2's
//     already-converted value would be shifted a second time.
//  2. Home's root moves out of the `system` KV table onto its root grid row
//     in the empty namespace, converted the same way, and the keys are
//     deleted. Zoom 0 means never visited and copies nothing.
//  3. tiles rebuilds, converting view_x and view_y through rebuildSelect.
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
// origin of a 1x1 synthetic doorway, so they convert by + 0.5; a missing or
// zero zoom means never visited, with nothing to carry.
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
		rootID, ok, err := systemValue(ctx, tx, systemKeyRootGridID)
		if err != nil {
			return fmt.Errorf("read home root grid id: %w", err)
		}
		if ok {
			if _, err := tx.ExecContext(ctx,
				`UPDATE grids SET root_cx = ?, root_cy = ?, root_zoom = ? WHERE id = ? AND ns = ''`,
				cx+0.5, cy+0.5, zoom, rootID); err != nil {
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

// addColumnDDL builds a run-func executing one additive DDL statement.
func addColumnDDL(ddl string) func(ctx context.Context, tx *sql.Tx) error {
	return func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, ddl)
		return err
	}
}

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
	// shape whatever a later template says; v12 drops it again. The two
	// partial indexes name the columns just added, so they ride here too, and
	// in Open's post-migration step for a fresh file, which never runs the
	// chain.
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

// migrateV10 retires the dead storage listed in the chain entry above. Order
// matters. grids.object_id goes first, by DROP COLUMN, which SQLite refuses
// while an index names the column, so that index goes first. Then the stale
// childless wells are adopted, each given a fresh empty child grid, because
// the rebuilt tiles CHECK no longer admits a well without one and dropping
// the user's tile is not an option. tiles goes last, through the shared
// rebuild, which saves and restores its seed.
func migrateV10(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS session`); err != nil {
		return fmt.Errorf("drop session table: %w", err)
	}
	// A file whose earlier rebuild materialized the current template has
	// neither the index nor the column: addColumnIfMissingDDL's case, from
	// the other side.
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
	// An old file still carries idx_tiles_object_id, which tilesIndexDDL does
	// not create. Drop it so the migrated and fresh index sets match.
	if _, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS idx_tiles_object_id`); err != nil {
		return fmt.Errorf("drop idx_tiles_object_id: %w", err)
	}
	if err := rebuildTiles(ctx, tx, 10); err != nil {
		return err
	}
	// DROP TABLE tiles took idx_tiles_live_key with it; externalsIndexDDL,
	// not tilesIndexDDL, creates it. Any rebuild after v9 must do the same.
	_, err = tx.ExecContext(ctx, externalsIndexDDL)
	return err
}

// adoptStalePluginWells turns every childless well carrying
// configure_plugin_id into an ordinary interior well by minting it a fresh
// empty child grid. Nothing can fill such a well any more, and the
// alternative to adoption is deleting a tile the user placed.
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

// hasColumn asks the migration steps' question of tableColumnFPs, the one
// reader of PRAGMA table_info.
func hasColumn(ctx context.Context, tx *sql.Tx, table, column string) (bool, error) {
	cols, err := tableColumnFPs(ctx, tx, table)
	if err != nil {
		return false, err
	}
	_, ok := cols[column]
	return ok, nil
}

// addColumnIfMissingDDL is addColumnDDL for a column added after a rebuild
// migration. A rebuild materializes the current tilesTableDDL, so a chain
// passing through it already carries every later column and a plain ALTER
// would fail; an older file whose rebuild ran under an older binary still
// needs it. Both paths converge, which TestSchemaEquivalence proves.
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

// applyMigrations brings the DB up to this binary's schemaVersion at Open.
func (s *Store) applyMigrations(ctx context.Context) error {
	return s.migrateUp(ctx, migrations, schemaVersion)
}

// migrateUp runs the pending entries of migs. The engine — the fresh stamp,
// the foreign-file and newer-version refusals, the chain — is
// internal/dbformat.EnsureVersion. migs and target are parameters rather than
// the globals so tests can drive a synthetic chain against a frozen-v1 DB.
func (s *Store) migrateUp(ctx context.Context, migs []migration, target int) error {
	chain := make([]dbformat.Migration, 0, len(migs))
	for _, m := range migs {
		chain = append(chain, dbformat.Migration{To: m.to, Run: m.run})
	}
	return dbformat.EnsureVersion(ctx, s.db, applicationID, target, chain)
}
