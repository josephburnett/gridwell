package store

import (
	"context"
	"fmt"
	"path/filepath"
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
// to a fresh Open (tablesTemplate). This is what makes the fresh-DB stamp
// shortcut in migrateUp sound — forget the inline tablesTemplate edit and the
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
