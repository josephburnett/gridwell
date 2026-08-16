package fs

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/josephburnett/gridwell/api/dbformat/dbformattest"
)

// TestFSSchemaEquivalence proves schemaTemplate == schemaV1 + fsMigrations —
// the soundness condition for the fresh-DB stamp in Open. Adding a column to
// the template without a migration (or vice versa) fails here loudly.
func TestFSSchemaEquivalence(t *testing.T) {
	dbformattest.AssertEquivalence(t, schemaTemplate, schemaV1, fsMigrations)
}

// TestFSFormatStampedAndReopens: a fresh file is stamped with the fs identity
// and version, and reopens cleanly (the contract check passes on its own
// output). Info reports the same generation the file carries.
func TestFSFormatStampedAndReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	p, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	var appID, ver int64
	if err := p.db.QueryRow(`PRAGMA application_id`).Scan(&appID); err != nil {
		t.Fatal(err)
	}
	if err := p.db.QueryRow(`PRAGMA user_version`).Scan(&ver); err != nil {
		t.Fatal(err)
	}
	if appID != fsApplicationID || ver != fsSchemaVersion {
		t.Errorf("stamped (app=%#x ver=%d), want (%#x, %d)", appID, ver, fsApplicationID, fsSchemaVersion)
	}
	info, err := p.Info(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if info.SchemaVersion != fsSchemaVersion {
		t.Errorf("Info.SchemaVersion = %d, want %d", info.SchemaVersion, fsSchemaVersion)
	}
	p.Close()

	p2, err := Open(path, nil)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	p2.Close()
}

// TestFSRefusesForeignAndNewerFiles: a file stamped by someone else, or by a
// future fs binary, is refused at Open rather than misread.
func TestFSRefusesForeignAndNewerFiles(t *testing.T) {
	foreign := filepath.Join(t.TempDir(), "foreign.db")
	p, err := Open(foreign, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec(`PRAGMA application_id = 195935983`); err != nil { // 0x0BADF00D
		t.Fatal(err)
	}
	p.Close()
	if _, err := Open(foreign, nil); err == nil {
		t.Error("Open must refuse a foreign application_id")
	}

	newer := filepath.Join(t.TempDir(), "newer.db")
	p, err = Open(newer, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec(`PRAGMA user_version = 999`); err != nil {
		t.Fatal(err)
	}
	p.Close()
	if _, err := Open(newer, nil); err == nil {
		t.Error("Open must refuse a newer-versioned file")
	}
}
