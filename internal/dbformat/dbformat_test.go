package dbformat

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func openMem(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func pragma(t *testing.T, db *sql.DB, name string) int64 {
	t.Helper()
	v, err := readPragmaInt(context.Background(), db, name)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

const testAppID = 0x54455354 // "TEST"

// A fresh, unstamped DB gets the caller's identity and target version.
func TestFreshDBStamped(t *testing.T) {
	db := openMem(t)
	if err := EnsureVersion(context.Background(), db, testAppID, 3, nil); err != nil {
		t.Fatal(err)
	}
	if got := pragma(t, db, "application_id"); got != testAppID {
		t.Errorf("application_id = %#x, want %#x", got, testAppID)
	}
	if got := pragma(t, db, "user_version"); got != 3 {
		t.Errorf("user_version = %d, want 3", got)
	}
	// Idempotent: a second Ensure at the same target is a no-op.
	if err := EnsureVersion(context.Background(), db, testAppID, 3, nil); err != nil {
		t.Fatal(err)
	}
}

// A file stamped by someone else is refused, never misread.
func TestForeignFileRefused(t *testing.T) {
	db := openMem(t)
	if err := setPragmaInt(context.Background(), db, "application_id", 0x0BADF00D); err != nil {
		t.Fatal(err)
	}
	err := EnsureVersion(context.Background(), db, testAppID, 1, nil)
	if err == nil || !strings.Contains(err.Error(), "application_id") {
		t.Fatalf("foreign file: err = %v, want an application_id refusal", err)
	}
}

// A file from a future binary is refused — an old binary must not misread a
// newer schema.
func TestNewerVersionRefused(t *testing.T) {
	db := openMem(t)
	if err := EnsureVersion(context.Background(), db, testAppID, 2, nil); err != nil {
		t.Fatal(err)
	}
	err := EnsureVersion(context.Background(), db, testAppID, 1, nil)
	if err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("newer file: err = %v, want a newer-version refusal", err)
	}
}

// An older file is brought forward by the pending chain entries, and rows
// written under the old shape survive.
func TestOlderFileMigrated(t *testing.T) {
	ctx := context.Background()
	db := openMem(t)
	// Build a genuine v1 file: old shape, stamped v1, with a row.
	if _, err := db.Exec(`CREATE TABLE things (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if err := EnsureVersion(ctx, db, testAppID, 1, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO things (name) VALUES ('kept')`); err != nil {
		t.Fatal(err)
	}

	ran := 0
	chain := []Migration{
		{To: 2, Run: func(ctx context.Context, tx *sql.Tx) error {
			ran++
			_, err := tx.ExecContext(ctx, `ALTER TABLE things ADD COLUMN color TEXT NOT NULL DEFAULT ''`)
			return err
		}},
	}
	if err := EnsureVersion(ctx, db, testAppID, 2, chain); err != nil {
		t.Fatal(err)
	}
	if ran != 1 {
		t.Fatalf("migrations run = %d, want 1", ran)
	}
	if got := pragma(t, db, "user_version"); got != 2 {
		t.Errorf("user_version = %d, want 2", got)
	}
	var name, color string
	if err := db.QueryRow(`SELECT name, color FROM things`).Scan(&name, &color); err != nil {
		t.Fatal(err)
	}
	if name != "kept" || color != "" {
		t.Errorf("old row after migration = (%q,%q), want (kept, empty default)", name, color)
	}
	// Re-Ensure at the same target runs nothing further.
	if err := EnsureVersion(ctx, db, testAppID, 2, chain); err != nil {
		t.Fatal(err)
	}
	if ran != 1 {
		t.Errorf("migration re-ran on an already-current file (%d runs)", ran)
	}
}

// A failing migration rolls back and leaves the version unstamped, so the
// next open retries rather than proceeding on a half-migrated file.
func TestFailedMigrationRollsBack(t *testing.T) {
	ctx := context.Background()
	db := openMem(t)
	if err := EnsureVersion(ctx, db, testAppID, 1, nil); err != nil {
		t.Fatal(err)
	}
	chain := []Migration{
		{To: 2, Run: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `THIS IS NOT SQL`)
			return err
		}},
	}
	if err := EnsureVersion(ctx, db, testAppID, 2, chain); err == nil {
		t.Fatal("failing migration must surface an error")
	}
	if got := pragma(t, db, "user_version"); got != 1 {
		t.Errorf("user_version after failed migration = %d, want still 1", got)
	}
}
