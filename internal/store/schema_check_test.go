package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// TestOpenRejectsLegacyBlobShape reproduces the silent "disappearing tile" bug:
// a pre-freeze DB whose blobs table still carries the long-removed `size`
// (NOT NULL, no default) column. The fast-path in migrateUp stamps such a DB as
// v1 without checking columns, so the divergence used to surface only when a
// blob insert hit the orphaned NOT NULL constraint — an error the client
// swallowed, making the tile vanish. Open must now reject it up front, naming
// the offending column.
func TestOpenRejectsLegacyBlobShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")

	// The pre-cebea98 blobs shape: size + created_at, which the current writer
	// (clone.go putBlob) no longer populates.
	const legacyBlobs = `
CREATE TABLE blobs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    hash       TEXT NOT NULL UNIQUE,
    size       INTEGER NOT NULL,
    data       BLOB NOT NULL,
    refcount   INTEGER NOT NULL DEFAULT 0,
    media_type TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL DEFAULT 0
);`
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(legacyBlobs); err != nil {
		t.Fatalf("create legacy blobs: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Open materializes the rest of the tables (blobs is left as-is by
	// CREATE TABLE IF NOT EXISTS), stamps the DB v1 — and the guard must catch
	// the divergent blobs shape.
	if _, err := Open(path); !errors.Is(err, ErrSchemaDivergence) {
		t.Fatalf("Open should reject the legacy blob shape with ErrSchemaDivergence, got: %v", err)
	} else if !strings.Contains(err.Error(), "blobs.size") {
		t.Errorf("error should name the offending column blobs.size; got: %v", err)
	}
}

// TestOpenAcceptsGenuineV1DB proves the guard does not false-positive on an
// in-contract database: a DB built at the frozen tablesV1 shape opens cleanly.
func TestOpenAcceptsGenuineV1DB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for _, ddl := range []string{pragmas, systemDDL, tablesV1, sessionDDL} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("apply v1 ddl: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("a genuine tablesV1 DB must open cleanly, got: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
}
