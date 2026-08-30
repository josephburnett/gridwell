package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strconv"
	"testing"
)

// TestVersionStampedOnFreshOpen confirms Open stamps the Gridwell
// application_id and the current schemaVersion into the SQLite header on a
// fresh DB. Without the stamp the next Open couldn't tell our file from a
// foreign one, and would re-run migrations from version 0.
func TestVersionStampedOnFreshOpen(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	appID, err := readPragmaInt(ctx, s.db, "application_id")
	if err != nil {
		t.Fatalf("read application_id: %v", err)
	}
	if appID != applicationID {
		t.Errorf("application_id = %#x, want %#x", appID, applicationID)
	}

	ver, err := readPragmaInt(ctx, s.db, "user_version")
	if err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if ver != schemaVersion {
		t.Errorf("user_version = %d, want %d", ver, schemaVersion)
	}
}

// TestApplyMigrationsRejectsNewerStoredVersion confirms an Open against a DB
// stamped with a higher user_version than this binary refuses to proceed —
// an older binary against a future-schema DB would silently misread rows.
func TestApplyMigrationsRejectsNewerStoredVersion(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.setPragmaInt(ctx, "user_version", schemaVersion+1); err != nil {
		t.Fatal(err)
	}
	if err := s.applyMigrations(ctx); err == nil {
		t.Fatalf("applyMigrations accepted a newer stored user_version")
	}
}

// TestApplyMigrationsRejectsForeignDatabase confirms Open refuses a SQLite
// file whose application_id isn't Gridwell's — protecting against pointing
// the server at an unrelated database.
func TestApplyMigrationsRejectsForeignDatabase(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.setPragmaInt(ctx, "application_id", 0x0BADF00D); err != nil {
		t.Fatal(err)
	}
	if err := s.applyMigrations(ctx); err == nil {
		t.Fatalf("applyMigrations accepted a foreign application_id")
	}
}

// TestMigrationsWellFormed enforces the bookkeeping that keeps the chain valid:
// migrations sorted and contiguous from 2, the last one equal to schemaVersion
// (empty ⟹ schemaVersion 1), and exactly one fixture per migration with aligned
// versions — so a migration added without a fixture, or with a non-contiguous
// version, fails the build.
func TestMigrationsWellFormed(t *testing.T) {
	for i, m := range migrations {
		if want := i + 2; m.to != want {
			t.Errorf("migrations[%d].to = %d, want %d (contiguous from 2)", i, m.to, want)
		}
	}
	if len(migrations) == 0 {
		if schemaVersion != 1 {
			t.Errorf("no migrations but schemaVersion = %d, want 1", schemaVersion)
		}
	} else if last := migrations[len(migrations)-1].to; last != schemaVersion {
		t.Errorf("last migration to = %d but schemaVersion = %d; they must match", last, schemaVersion)
	}
	if len(migrationFixtures) != len(migrations) {
		t.Fatalf("have %d migrations but %d fixtures; each migration needs exactly one fixture",
			len(migrations), len(migrationFixtures))
	}
	for i := range migrations {
		if migrationFixtures[i].version != migrations[i].to {
			t.Errorf("fixture[%d].version = %d, migration.to = %d", i, migrationFixtures[i].version, migrations[i].to)
		}
	}
}

// TestSchemaEquivalence is the no-drift binding: a DB built from the frozen
// tablesV1 base and walked through every migration must end up schema-identical
// to a fresh Open (tablesDDL(), rendered from the column descriptor). This is
// what makes the fresh-DB stamp shortcut in migrateUp sound — forget the
// descriptor entry and the
// fresh side lacks the column; forget the migration and the migrated side does.
func TestSchemaEquivalence(t *testing.T) {
	db, _ := buildDBAtV1(t, filepath.Join(t.TempDir(), "migrated.db"))
	applyMigrationsUpTo(t, db, schemaVersion)
	migrated := schemaFingerprint(t, db)

	fresh, _ := newTestStoreFile(t)
	compareFingerprints(t, schemaFingerprint(t, fresh.db), migrated)
}

// TestMigrationChain walks a single v1 DB through the whole chain in order,
// asserting representative v1 data (text, url, well + child grid) reads back
// unchanged after every step, and that the final schema equals a fresh DB.
func TestMigrationChain(t *testing.T) {
	db, root := buildDBAtV1(t, filepath.Join(t.TempDir(), "chain.db"))
	fx := seedV1(t, db, root)
	verifyV1Survived(t, db, fx) // intact at v1

	for v := 2; v <= schemaVersion; v++ {
		applyMigrationsUpTo(t, db, v)
		verifyV1Survived(t, db, fx) // intact after each step
	}

	fresh, _ := newTestStoreFile(t)
	compareFingerprints(t, schemaFingerprint(t, fresh.db), schemaFingerprint(t, db))
}

// TestPerMigration tests each migration in isolation: build at the version just
// before it, seed rows valid there, apply only that one migration, then verify
// its schema change landed and the pre-existing rows survived. No-op until the
// first post-v1 migration adds a fixture.
func TestPerMigration(t *testing.T) {
	for _, f := range migrationFixtures {
		f := f
		t.Run(fmt.Sprintf("v%d", f.version), func(t *testing.T) {
			db, root := buildDBAtV1(t, filepath.Join(t.TempDir(), "permig.db"))
			applyMigrationsUpTo(t, db, f.version-1)
			if f.seed != nil {
				f.seed(t, db, root)
			}
			applyMigrationsUpTo(t, db, f.version)
			if f.verify != nil {
				f.verify(t, db)
			}
		})
	}
}

// TestMigrateUpRunsRealMigrations exercises the production engine end-to-end
// with a synthetic two-step chain against a frozen-v1 file: real ALTER TABLE
// migrations through Store.migrateUp must add their columns, stamp user_version,
// preserve all pre-existing data, and be idempotent on re-run. This proves the
// upgrade machinery works today, before any real post-v1 migration exists.
func TestMigrateUpRunsRealMigrations(t *testing.T) {
	ctx := context.Background()
	db, root := buildDBAtV1(t, filepath.Join(t.TempDir(), "engine.db"))
	fx := seedV1(t, db, root)
	s := storeOver(db)

	migs := []migration{
		{to: 2, run: addColumn(`ALTER TABLE tiles ADD COLUMN note TEXT`)},
		{to: 3, run: addColumn(`ALTER TABLE blobs ADD COLUMN byte_size INTEGER NOT NULL DEFAULT 0`)},
	}
	if err := s.migrateUp(ctx, migs, 3); err != nil {
		t.Fatalf("migrateUp: %v", err)
	}

	assertColumn(t, db, "tiles", "note")
	assertColumn(t, db, "blobs", "byte_size")

	if uv, err := readPragmaInt(ctx, db, "user_version"); err != nil {
		t.Fatal(err)
	} else if uv != 3 {
		t.Errorf("user_version = %d after migrating to 3", uv)
	}

	verifyV1Survived(t, db, fx)

	// Idempotent: re-running to the same target is a no-op (user_version==target).
	if err := s.migrateUp(ctx, migs, 3); err != nil {
		t.Fatalf("migrateUp re-run: %v", err)
	}
}

// TestMigrateV10OverAGenuineV9File covers what the per-migration fixture
// cannot reach. A file written by a v9 BINARY carries tiles.object_id, its
// index, tiles.configure_plugin_id, and a CHECK that admits a CHILDLESS well
// — the unconfigured plugin well (#251). A chain-built v9 file carries none
// of those: the v5 rebuild already materializes the CURRENT tiles shape (the
// convergence contract), so there was nothing there to plant a row in. Here
// the genuine v9 shape is put back by hand and v10 is run over it.
//
// The invariant under test is the guiding rule: a tile the user placed must
// still be there afterwards, at the SAME id, and must now work — deleting it
// because its feature retired would be a change the user did not make.
func TestMigrateV10OverAGenuineV9File(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "genuinev9.db")
	db, root := buildDBAtV1(t, path)
	applyMigrationsUpTo(t, db, 9)

	// The v9 columns and index a chain-built file no longer has. (object_id
	// was TEXT NOT NULL with no default on a real v9 file; the added default
	// only lets this test insert without naming it — v10 never copies the
	// column either way.)
	for _, ddl := range []string{
		`ALTER TABLE tiles ADD COLUMN object_id TEXT NOT NULL DEFAULT ''`,
		`CREATE INDEX idx_tiles_object_id ON tiles(object_id)`,
		`ALTER TABLE tiles ADD COLUMN configure_plugin_id TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			t.Fatalf("restore the v9 shape (%s): %v", ddl, err)
		}
	}
	if _, err := db.ExecContext(ctx, `PRAGMA ignore_check_constraints = true`); err != nil {
		t.Fatalf("suspend the CHECK: %v", err)
	}
	res, err := db.ExecContext(ctx,
		`INSERT INTO tiles (grid_id, kind, x, y, w, h, configure_plugin_id, alt_text, created_at, updated_at)
		 VALUES (`+root+`, 'well', 3, 3, 2, 2, 'sshpluginuuid', 'gpu box', 100, 100)`)
	if err != nil {
		t.Fatalf("plant the v8 unconfigured plugin well: %v", err)
	}
	staleID := mustID(t, res)
	if _, err := db.ExecContext(ctx, `PRAGMA ignore_check_constraints = false`); err != nil {
		t.Fatalf("restore the CHECK: %v", err)
	}

	if err := storeOver(db).migrateUp(ctx, migrations, 10); err != nil {
		t.Fatalf("migrate to v10 over a stale plugin well: %v", err)
	}

	var child sql.NullInt64
	var alt string
	var x, y, w, h int64
	if err := db.QueryRowContext(ctx,
		`SELECT child_grid_id, alt_text, x, y, w, h FROM tiles WHERE id = ?`, staleID).
		Scan(&child, &alt, &x, &y, &w, &h); err != nil {
		t.Fatalf("the stale well was DELETED by v10, not adopted: %v", err)
	}
	if !child.Valid {
		t.Fatal("the adopted well has no child grid; the new CHECK could not have admitted it")
	}
	if alt != "gpu box" || x != 3 || y != 3 || w != 2 || h != 2 {
		t.Errorf("the adopted well moved or was renamed: alt=%q at (%d,%d %dx%d)", alt, x, y, w, h)
	}
	var nGrid, nTiles int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM grids WHERE id = ?`, child.Int64).Scan(&nGrid); err != nil {
		t.Fatal(err)
	}
	if nGrid != 1 {
		t.Error("the minted child grid is missing")
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tiles WHERE grid_id = ?`, child.Int64).Scan(&nTiles); err != nil {
		t.Fatal(err)
	}
	if nTiles != 0 {
		t.Errorf("the minted child grid holds %d tiles, want an empty room", nTiles)
	}
	// Both v9 columns and the index went with the migration.
	for _, tbl := range []string{"grids", "tiles"} {
		if _, ok := tableColumnsFP(t, db, tbl)[objectIDColumn]; ok {
			t.Errorf("%s.object_id survived v10 on a genuine v9 file", tbl)
		}
	}
	if _, ok := tableColumnsFP(t, db, "tiles")["configure_plugin_id"]; ok {
		t.Error("tiles.configure_plugin_id survived v10 on a genuine v9 file")
	}
	var nIdx int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'index' AND name = 'idx_tiles_object_id'`).Scan(&nIdx); err != nil {
		t.Fatal(err)
	}
	if nIdx != 0 {
		t.Error("idx_tiles_object_id survived v10 on a genuine v9 file")
	}
	// And the file OPENS through the production door: Open re-runs the
	// chain (a no-op now), passes verifySchema, and reads the adopted well
	// back as an ordinary tile. This is the storage promise itself — a file
	// a released binary wrote still opens and still holds what the user put
	// in it.
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("a genuine v9 file must open after v10: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	reopened, err := s.GetTile(ctx, strconv.FormatInt(staleID, 10))
	if err != nil {
		t.Fatalf("read the adopted well after reopen: %v", err)
	}
	if reopened.Kind != "well" || reopened.ChildGridID == "" || reopened.AltText != "gpu box" {
		t.Errorf("adopted well after reopen = %+v, want a named well with a child grid", reopened)
	}
}

// TestMigrateV11OverAGenuineV10File covers what the per-migration fixture
// cannot reach: the framing CONVERSION itself. A chain-built v10 file
// already carries view_cx/view_cy (the v5 rebuild materializes the
// CURRENT tiles shape — the convergence contract), so there is no
// view_x/view_y in it to convert. A file written by a v10 BINARY has the
// integer window ORIGIN, and every well in it must come out of v11
// showing EXACTLY the framing it showed before: the center the client
// derived to display it, origin + footprint/2.
func TestMigrateV11OverAGenuineV10File(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "genuinev10.db")
	db, root := buildDBAtV1(t, path)
	applyMigrationsUpTo(t, db, 10)

	// Put the v10 tiles shape back: the integer origin pair, in place of
	// the float center pair a converged file already has.
	for _, ddl := range []string{
		`ALTER TABLE tiles DROP COLUMN view_cx`,
		`ALTER TABLE tiles DROP COLUMN view_cy`,
		`ALTER TABLE tiles ADD COLUMN view_x INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE tiles ADD COLUMN view_y INTEGER NOT NULL DEFAULT 0`,
	} {
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			t.Fatalf("restore the v10 shape (%s): %v", ddl, err)
		}
	}
	res, err := db.ExecContext(ctx, `INSERT INTO grids (created_at, updated_at) VALUES (100, 100)`)
	if err != nil {
		t.Fatal(err)
	}
	child := mustID(t, res)
	// An even footprint (the center lands on a whole cell) and an odd one
	// (the center is a HALF cell — the value the integer column could not
	// hold, and the reason ViewOriginFromCenter had to exist).
	for _, w := range []struct {
		alt    string
		w, h   int64
		vx, vy int64
		zoom   float64
		wantX  float64
		wantY  float64
	}{
		{"v10 even well", 2, 4, 7, -3, 0.375, 8, -1},
		{"v10 odd well", 1, 1, 4, 4, 0.125, 4.5, 4.5},
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO tiles
			(grid_id, kind, x, y, w, h, view_x, view_y, view_zoom, child_grid_id, alt_text, created_at, updated_at)
			VALUES (`+root+`, 'well', 0, 0, ?, ?, ?, ?, ?, ?, ?, 100, 100)`,
			w.w, w.h, w.vx, w.vy, w.zoom, child, w.alt); err != nil {
			t.Fatalf("plant %s: %v", w.alt, err)
		}
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE system SET value = '12' WHERE key = 'root_view_cx'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE system SET value = '0.5' WHERE key = 'root_zoom'`); err != nil {
		t.Fatal(err)
	}

	if err := storeOver(db).migrateUp(ctx, migrations, 11); err != nil {
		t.Fatalf("migrate a genuine v10 file to v11: %v", err)
	}

	for _, want := range []struct {
		alt    string
		cx, cy float64
		zoom   float64
	}{
		{"v10 even well", 8, -1, 0.375},
		{"v10 odd well", 4.5, 4.5, 0.125},
	} {
		var cx, cy, zoom float64
		if err := db.QueryRowContext(ctx,
			`SELECT view_cx, view_cy, view_zoom FROM tiles WHERE alt_text = ?`, want.alt).
			Scan(&cx, &cy, &zoom); err != nil {
			t.Fatalf("read %s after v11: %v", want.alt, err)
		}
		if cx != want.cx || cy != want.cy || zoom != want.zoom {
			t.Errorf("%s framing = (%v, %v, %v), want the center it displayed at (%v, %v, %v)",
				want.alt, cx, cy, zoom, want.cx, want.cy, want.zoom)
		}
	}
	for _, col := range []string{"view_x", "view_y"} {
		if _, ok := tableColumnsFP(t, db, "tiles")[col]; ok {
			t.Errorf("tiles.%s survived v11 on a genuine v10 file", col)
		}
	}
	// Home's root came across too, converted the same way.
	var cx, zoom float64
	if err := db.QueryRowContext(ctx,
		`SELECT root_cx, root_zoom FROM grids WHERE id = `+root+` AND ns = ''`).Scan(&cx, &zoom); err != nil {
		t.Fatalf("read home root framing: %v", err)
	}
	if cx != 12.5 || zoom != 0.5 {
		t.Errorf("home root framing = (%v, zoom %v), want (12.5, 0.5)", cx, zoom)
	}

	// And the file OPENS through the production door and reads the wells
	// back at their preserved framing — the storage promise itself.
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("a genuine v10 file must open after v11: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	g, err := s.GetGrid(ctx, root)
	if err != nil {
		t.Fatalf("read the root grid after reopen: %v", err)
	}
	found := 0
	for _, tile := range g.Tiles {
		if tile.AltText == "v10 odd well" {
			found++
			if tile.ViewCx != 4.5 || tile.ViewCy != 4.5 {
				t.Errorf("odd well after reopen = (%v, %v), want (4.5, 4.5)", tile.ViewCx, tile.ViewCy)
			}
		}
	}
	if found != 1 {
		t.Errorf("the odd well is not in the reopened grid (%d matches)", found)
	}
}
