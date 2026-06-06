package store

import (
	"context"
	"strconv"
	"testing"
)

// TestSchemaVersionStampedOnFreshOpen confirms Open stamps the
// schema_version into the system table on a fresh DB. Without that
// stamp the next Open would walk every migration from version 0,
// reapplying schema-baseline DDL twice.
func TestSchemaVersionStampedOnFreshOpen(t *testing.T) {
	s := newTestStore(t)
	var v string
	err := s.db.QueryRowContext(context.Background(),
		`SELECT value FROM system WHERE key = ?`, systemKeySchemaVersion).Scan(&v)
	if err != nil {
		t.Fatalf("schema_version not stamped on fresh open: %v", err)
	}
	got, err := strconv.Atoi(v)
	if err != nil {
		t.Fatalf("schema_version not numeric: %q", v)
	}
	if got != schemaVersion {
		t.Errorf("stamped schema_version = %d, want %d", got, schemaVersion)
	}
}

// TestApplyMigrationsRejectsNewerStoredVersion confirms an Open against
// a DB stamped with a higher schemaVersion than this binary refuses to
// proceed — running an older binary against a future-schema DB would
// silently miscread rows.
func TestApplyMigrationsRejectsNewerStoredVersion(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx,
		`UPDATE system SET value = ? WHERE key = ?`,
		strconv.Itoa(schemaVersion+1), systemKeySchemaVersion); err != nil {
		t.Fatal(err)
	}
	if err := s.applyMigrations(ctx); err == nil {
		t.Fatalf("applyMigrations accepted a newer stored schema_version")
	}
}
