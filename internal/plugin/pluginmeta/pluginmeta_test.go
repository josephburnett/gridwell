package pluginmeta

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func dbpath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "meta.db")
}

func TestEnsureFirstRunRecordsIdentity(t *testing.T) {
	p := dbpath(t)
	m, err := Ensure(p, "id-1", "localdb")
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if m.ID != "id-1" || m.Kind != "localdb" {
		t.Fatalf("returned meta: %+v", m)
	}
	// The gridwell marker must be present so the file is identifiable.
	db, _ := sql.Open("sqlite", p)
	defer db.Close()
	var marker string
	if err := db.QueryRow(`SELECT v FROM _gridwell_meta WHERE k = 'gridwell'`).Scan(&marker); err != nil {
		t.Fatalf("marker missing: %v", err)
	}
}

func TestEnsureMatchingReRunOK(t *testing.T) {
	p := dbpath(t)
	if _, err := Ensure(p, "id-1", "localdb"); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := Ensure(p, "id-1", "localdb"); err != nil {
		t.Fatalf("matching re-run must succeed: %v", err)
	}
}

func TestEnsureIDMismatch(t *testing.T) {
	p := dbpath(t)
	if _, err := Ensure(p, "id-1", "localdb"); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := Ensure(p, "id-2", "localdb"); !errors.Is(err, ErrIDMismatch) {
		t.Fatalf("id change must be rejected, got: %v", err)
	}
}

func TestEnsureKindMismatch(t *testing.T) {
	p := dbpath(t)
	if _, err := Ensure(p, "id-1", "localdb"); err != nil {
		t.Fatalf("first: %v", err)
	}
	// Same id, different kind — the schema would be wrong; refuse.
	if _, err := Ensure(p, "id-1", "fs"); !errors.Is(err, ErrKindMismatch) {
		t.Fatalf("kind change must be rejected, got: %v", err)
	}
}

func TestEnsureReadOnlyProbe(t *testing.T) {
	p := dbpath(t)
	if _, err := Ensure(p, "id-1", "proc"); err != nil {
		t.Fatalf("first: %v", err)
	}
	m, err := Ensure(p, "", "")
	if err != nil {
		t.Fatalf("read-only probe: %v", err)
	}
	if m.ID != "id-1" || m.Kind != "proc" {
		t.Fatalf("probe returned: %+v", m)
	}
}

// TestEnsureLegacyUUIDPreserved proves a DB created before the id/kind split
// (identity under the legacy "uuid" key) keeps its identity: the same id is
// accepted (not re-minted) and a different id is rejected.
func TestEnsureLegacyUUIDPreserved(t *testing.T) {
	p := dbpath(t)
	db, _ := sql.Open("sqlite", p)
	if _, err := db.Exec(`CREATE TABLE _gridwell_meta (k TEXT PRIMARY KEY, v TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO _gridwell_meta (k, v) VALUES ('uuid', 'legacy-id')`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	if _, err := Ensure(p, "legacy-id", "localdb"); err != nil {
		t.Fatalf("legacy id must be accepted: %v", err)
	}
	if _, err := Ensure(p, "different", "localdb"); !errors.Is(err, ErrIDMismatch) {
		t.Fatalf("a different id against a legacy DB must be rejected, got: %v", err)
	}
}
