package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The disappearing-tile class: an unstamped DB whose blobs table still carries
// a NOT NULL `size` column with no default. The migrateUp fast path stamps
// such a DB as v1 without checking columns, so without the guard the
// divergence surfaces as a tile that vanished. Open rejects it up front,
// naming the column.
func TestOpenRejectsLegacyBlobShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")

	// An out-of-contract blobs shape: size and created_at, which the current
	// writer, putBlob in copy.go, does not populate.
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
	for _, ddl := range []string{pragmas, systemDDL, tablesV1, sessionDDLV1} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("apply v1 ddl: %v", err)
		}
	}
	// A DB genuinely written by a v1 binary carries both header stamps; without
	// them this fixture only impersonated v1 while no migrations existed (the
	// unstamped file was silently treated as fresh).
	for _, stamp := range []string{
		"PRAGMA application_id = " + strconv.Itoa(applicationID),
		"PRAGMA user_version = 1",
	} {
		if _, err := db.Exec(stamp); err != nil {
			t.Fatalf("stamp v1 header: %v", err)
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
