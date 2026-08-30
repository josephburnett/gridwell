package pluginmeta

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

// There is deliberately no sqlite driver import here: the production package
// must register the driver it uses itself. A blank import in this test file
// would mask a missing registration, leaving every spawn failing with `unknown
// driver "sqlite"` while this suite stayed green.

func dbpath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "meta.db")
}

func TestCreateRecordsIdentity(t *testing.T) {
	p := dbpath(t)
	if err := Create(p, "id-1", "home"); err != nil {
		t.Fatalf("create: %v", err)
	}
	// The gridwell marker must be present so the file is identifiable.
	db, _ := sql.Open("sqlite", p)
	defer db.Close()
	var marker string
	if err := db.QueryRow(`SELECT v FROM _gridwell_meta WHERE k = 'gridwell'`).Scan(&marker); err != nil {
		t.Fatalf("marker missing: %v", err)
	}
	// And Verify reads the identity back.
	m, err := Verify(p, "", "")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if m.ID != "id-1" || m.Kind != "home" {
		t.Fatalf("stored identity: %+v", m)
	}
}

func TestVerifyMatchingOK(t *testing.T) {
	p := dbpath(t)
	if err := Create(p, "id-1", "home"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := Verify(p, "id-1", "home"); err != nil {
		t.Fatalf("matching verify must succeed: %v", err)
	}
}

func TestVerifyIDMismatch(t *testing.T) {
	p := dbpath(t)
	if err := Create(p, "id-1", "home"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := Verify(p, "id-2", "home"); !errors.Is(err, ErrIDMismatch) {
		t.Fatalf("id change must be rejected, got: %v", err)
	}
}

func TestVerifyKindMismatch(t *testing.T) {
	p := dbpath(t)
	if err := Create(p, "id-1", "home"); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Same id, different kind: the schema would be wrong, so refuse.
	if _, err := Verify(p, "id-1", "fs"); !errors.Is(err, ErrKindMismatch) {
		t.Fatalf("kind change must be rejected, got: %v", err)
	}
}

func TestVerifyReadOnlyProbe(t *testing.T) {
	p := dbpath(t)
	if err := Create(p, "id-1", "proc"); err != nil {
		t.Fatalf("create: %v", err)
	}
	m, err := Verify(p, "", "")
	if err != nil {
		t.Fatalf("read-only probe: %v", err)
	}
	if m.ID != "id-1" || m.Kind != "proc" {
		t.Fatalf("probe returned: %+v", m)
	}
}

// TestVerifyUninitialized pins that Verify never creates identity. A missing
// file and a DB with no stored identity both fail with ErrNotInitialized, so a
// config entry whose DB was never created cannot silently open a fresh
// store.
func TestVerifyUninitialized(t *testing.T) {
	// Missing file.
	if _, err := Verify(dbpath(t), "id-1", "home"); !errors.Is(err, ErrNotInitialized) {
		t.Errorf("missing DB must be ErrNotInitialized, got: %v", err)
	}
	// An existing but empty DB, with no _gridwell_meta identity.
	p := dbpath(t)
	db, _ := sql.Open("sqlite", p)
	if _, err := db.Exec(`CREATE TABLE t (x)`); err != nil { // make the file exist
		t.Fatal(err)
	}
	db.Close()
	if _, err := Verify(p, "id-1", "home"); !errors.Is(err, ErrNotInitialized) {
		t.Errorf("uninitialized DB must be ErrNotInitialized, got: %v", err)
	}
}

// TestVerifyLegacyUUIDPreserved proves a DB whose identity is under the older
// "uuid" key keeps that identity: the same id is accepted rather than
// re-minted, and a different id is rejected.
func TestVerifyLegacyUUIDPreserved(t *testing.T) {
	p := dbpath(t)
	db, _ := sql.Open("sqlite", p)
	if _, err := db.Exec(`CREATE TABLE _gridwell_meta (k TEXT PRIMARY KEY, v TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO _gridwell_meta (k, v) VALUES ('uuid', 'legacy-id')`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	if _, err := Verify(p, "legacy-id", "home"); err != nil {
		t.Fatalf("legacy id must be accepted: %v", err)
	}
	if _, err := Verify(p, "different", "home"); !errors.Is(err, ErrIDMismatch) {
		t.Fatalf("a different id against a legacy DB must be rejected, got: %v", err)
	}
}
