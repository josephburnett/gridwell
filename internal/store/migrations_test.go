package store

import (
	"context"
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
