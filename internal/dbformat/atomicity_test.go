package dbformat

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// These tests pin the crash-safety property of EnsureVersion: the
// user_version stamp must be part of the SAME transaction as the work it
// describes. SQLite header pragmas are transactional, so this is free —
// but the original code committed the (non-idempotent, ALTER TABLE ADD
// COLUMN) migrations first and stamped afterwards. A crash in that window
// left a file with the new columns and the old version: every subsequent
// Open re-ran the chain, failed on "duplicate column name", and the DB —
// forever-data by contract — became permanently unopenable. Same window on
// the fresh-DB path between the application_id and user_version writes.
//
// A crash between two statements can't be triggered deterministically from a
// test, so the property is asserted at the statement level instead: a
// recording driver logs every statement plus BEGIN/COMMIT, and the test
// requires the stamp to appear between them.

// ── recording driver ─────────────────────────────────────────────────────────

type recDriver struct {
	inner driver.Driver
	mu    *sync.Mutex
	log   *[]string
}

func (d *recDriver) Open(name string) (driver.Conn, error) {
	c, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &recConn{inner: c, mu: d.mu, log: d.log}, nil
}

// recConn implements ONLY Prepare/Begin/Close, so database/sql funnels every
// statement through Prepare (no Execer/Queryer fast paths) and each one is
// logged. Begin/Commit are logged via recTx.
type recConn struct {
	inner driver.Conn
	mu    *sync.Mutex
	log   *[]string
}

func (c *recConn) record(s string) {
	c.mu.Lock()
	*c.log = append(*c.log, s)
	c.mu.Unlock()
}

func (c *recConn) Prepare(query string) (driver.Stmt, error) {
	c.record(query)
	return c.inner.Prepare(query)
}

func (c *recConn) Close() error { return c.inner.Close() }

func (c *recConn) Begin() (driver.Tx, error) {
	c.record("BEGIN")
	//lint:ignore SA1019 the deprecated fallback path is fine for a test double
	tx, err := c.inner.Begin()
	if err != nil {
		return nil, err
	}
	return &recTx{inner: tx, conn: c}, nil
}

type recTx struct {
	inner driver.Tx
	conn  *recConn
}

func (t *recTx) Commit() error {
	t.conn.record("COMMIT")
	return t.inner.Commit()
}

func (t *recTx) Rollback() error {
	t.conn.record("ROLLBACK")
	return t.inner.Rollback()
}

var recRegister sync.Once
var recMu sync.Mutex
var recLog []string

func openRecordingDB(t *testing.T) *sql.DB {
	t.Helper()
	// Steal the real driver from a throwaway handle.
	probe, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	inner := probe.Driver()
	probe.Close()
	recRegister.Do(func() {
		sql.Register("sqlite-recording", &recDriver{inner: inner, mu: &recMu, log: &recLog})
	})
	db, err := sql.Open("sqlite-recording", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func takeLog() []string {
	recMu.Lock()
	defer recMu.Unlock()
	out := append([]string(nil), recLog...)
	recLog = recLog[:0]
	return out
}

func indexContaining(log []string, substr string) int {
	for i, s := range log {
		if strings.Contains(s, substr) {
			return i
		}
	}
	return -1
}

// ── the properties ───────────────────────────────────────────────────────────

// The migrate path: the user_version stamp must execute inside the migration
// transaction (after the migration statements, before COMMIT), so a crash can
// never persist the DDL without the version that records it.
func TestMigrationStampIsInsideTheTransaction(t *testing.T) {
	ctx := context.Background()
	db := openRecordingDB(t)

	// An already-stamped v1 file awaiting one migration.
	if err := setPragmaInt(ctx, db, "application_id", testAppID); err != nil {
		t.Fatal(err)
	}
	if err := setPragmaInt(ctx, db, "user_version", 1); err != nil {
		t.Fatal(err)
	}
	takeLog()

	mig := Migration{To: 2, Run: func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `CREATE TABLE migrated (id INTEGER)`)
		return err
	}}
	if err := EnsureVersion(ctx, db, testAppID, 2, []Migration{mig}); err != nil {
		t.Fatal(err)
	}
	log := takeLog()

	begin := indexContaining(log, "BEGIN")
	ddl := indexContaining(log, "CREATE TABLE migrated")
	stamp := indexContaining(log, "user_version = 2")
	commit := indexContaining(log, "COMMIT")
	if begin < 0 || ddl < 0 || stamp < 0 || commit < 0 {
		t.Fatalf("expected BEGIN, DDL, stamp, COMMIT in the statement log; got:\n%s", strings.Join(log, "\n"))
	}
	if !(begin < ddl && ddl < stamp && stamp < commit) {
		t.Errorf("user_version stamp is not inside the migration transaction (crash window: a migrated-but-unstamped file re-runs non-idempotent migrations forever).\norder: BEGIN@%d DDL@%d stamp@%d COMMIT@%d\nlog:\n%s",
			begin, ddl, stamp, commit, strings.Join(log, "\n"))
	}
	// And the result is right.
	if got := pragma(t, db, "user_version"); got != 2 {
		t.Errorf("user_version = %d, want 2", got)
	}
}

// The fresh-DB path: application_id and user_version must land atomically. A
// crash between them leaves application_id stamped with user_version 0, so
// the next Open runs the FULL migration chain against the latest-shape tables
// — the same duplicate-column brick.
func TestFreshStampIsAtomic(t *testing.T) {
	ctx := context.Background()
	db := openRecordingDB(t)
	takeLog()

	if err := EnsureVersion(ctx, db, testAppID, 3, nil); err != nil {
		t.Fatal(err)
	}
	log := takeLog()

	appIdx := indexContaining(log, fmt.Sprintf("application_id = %d", testAppID))
	verIdx := indexContaining(log, "user_version = 3")
	begin := indexContaining(log, "BEGIN")
	commit := indexContaining(log, "COMMIT")
	if appIdx < 0 || verIdx < 0 {
		t.Fatalf("expected both stamps in the statement log; got:\n%s", strings.Join(log, "\n"))
	}
	if begin < 0 || commit < 0 || !(begin < appIdx && appIdx < commit && begin < verIdx && verIdx < commit) {
		t.Errorf("fresh-DB identity stamps are not in one transaction (crash window: application_id without user_version re-runs the full chain on a latest-shape DB).\nlog:\n%s",
			strings.Join(log, "\n"))
	}
}
