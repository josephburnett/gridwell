package sshhost

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
)

func openTestDB(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ssh.db")
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, path
}

func TestMintedSegmentShapeAndVersionDiscipline(t *testing.T) {
	ctx := context.Background()
	db, _ := openTestDB(t)

	c, err := db.Create(ctx, 1, 2, 3, 4, "")
	if err != nil {
		t.Fatal(err)
	}
	// The namespace segment must obey the URL grammar: letter-leading, never
	// parseable as an integer (how paths tell a namespace from a tile id).
	if c.NS == "" || c.NS[0] < 'a' || c.NS[0] > 'z' {
		t.Fatalf("ns %q must start with a lowercase letter", c.NS)
	}
	if _, err := strconv.ParseInt(c.NS, 10, 64); err == nil {
		t.Fatalf("ns %q parses as an integer", c.NS)
	}
	if c.ObjectID == "" {
		t.Error("a connection carries a provenance object_id")
	}
	if c.Version != 1 {
		t.Fatalf("fresh version = %d, want 1", c.Version)
	}

	// Params commit is a content edit: bumps. Framing never bumps. Rename
	// bumps and latches. Placement bumps (the store's PlaceTile discipline).
	c, err = db.SetParams(ctx, c.ID, 1, `{"host":"h","user":"u"}`)
	if err != nil {
		t.Fatal(err)
	}
	if c.Version != 2 {
		t.Errorf("params: version = %d, want 2", c.Version)
	}
	if _, err := db.SetParams(ctx, c.ID, 1, `{}`); err == nil {
		t.Error("a stale claim must be refused")
	}
	c, err = db.SetFraming(ctx, c.ID, 9, 9, 1.5)
	if err != nil {
		t.Fatal(err)
	}
	if c.Version != 2 {
		t.Errorf("framing bumped: version = %d, want 2", c.Version)
	}
	c, err = db.Rename(ctx, c.ID, 2, "named")
	if err != nil {
		t.Fatal(err)
	}
	if c.Version != 3 || !c.AltUser {
		t.Errorf("rename: version = %d altUser = %v, want 3/true", c.Version, c.AltUser)
	}
	c, err = db.SetPlacement(ctx, c.ID, 3, 5, 6, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if c.Version != 4 {
		t.Errorf("placement: version = %d, want 4", c.Version)
	}
}

func TestParamsChangeClearsTheCachedRoot(t *testing.T) {
	ctx := context.Background()
	db, _ := openTestDB(t)
	c, _ := db.Create(ctx, 0, 0, 1, 1, "")
	c, _ = db.SetParams(ctx, c.ID, c.Version, `{"host":"a","user":"u"}`)
	c, err := db.SetRemoteRoot(ctx, c.ID, "rnode/0")
	if err != nil || c.RemoteRoot != "rnode/0" {
		t.Fatalf("remote root not cached: %v %+v", err, c)
	}
	// New params may name a different machine — the cache must not survive.
	c, err = db.SetParams(ctx, c.ID, c.Version, `{"host":"b","user":"u"}`)
	if err != nil {
		t.Fatal(err)
	}
	if c.RemoteRoot != "" {
		t.Errorf("stale remote root survived a params change: %q", c.RemoteRoot)
	}
}

func TestTombstoneReservesTheNamespaceForever(t *testing.T) {
	ctx := context.Background()
	db, path := openTestDB(t)
	c, _ := db.Create(ctx, 0, 0, 1, 1, "")
	if err := db.Tombstone(ctx, c.ID, c.Version); err != nil {
		t.Fatal(err)
	}
	// The row STAYS: the segment resolves (as deleted), never to a stranger.
	got, err := db.GetByNS(ctx, c.NS)
	if err != nil {
		t.Fatalf("a tombstoned ns must still resolve: %v", err)
	}
	if !got.Deleted {
		t.Error("tombstone lost")
	}
	// Not listed.
	list, _ := db.List(ctx)
	if len(list) != 0 {
		t.Errorf("tombstoned row listed: %d", len(list))
	}
	// The numeric id is never reused (AUTOINCREMENT).
	c2, _ := db.Create(ctx, 5, 5, 1, 1, "")
	if c2.ID <= c.ID {
		t.Errorf("id %d reused after tombstone of %d", c2.ID, c.ID)
	}

	// Everything survives a reopen — connections are durable data.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db2, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	got, err = db2.GetByNS(ctx, c.NS)
	if err != nil || !got.Deleted {
		t.Fatalf("tombstone must survive reopen: %v %+v", err, got)
	}
	live, err := db2.Get(ctx, c2.ID)
	if err != nil || live.Deleted {
		t.Fatalf("live row must survive reopen: %v", err)
	}
}

func TestRootViewRoundTrip(t *testing.T) {
	ctx := context.Background()
	db, _ := openTestDB(t)
	cx, cy, zoom, err := db.RootView(ctx)
	if err != nil || cx != 0 || cy != 0 || zoom != 0 {
		t.Fatalf("fresh root view: %v %v %v %v", cx, cy, zoom, err)
	}
	if err := db.SetRootView(ctx, 1.5, -2.25, 0.75); err != nil {
		t.Fatal(err)
	}
	cx, cy, zoom, err = db.RootView(ctx)
	if err != nil || cx != 1.5 || cy != -2.25 || zoom != 0.75 {
		t.Fatalf("root view round trip: %v %v %v %v", cx, cy, zoom, err)
	}
}

// TestConcurrentWritesNeverBusy reproduces the 2026-08-08 `make check` flake:
// OpenDB used database/sql's default pool, so two pooled connections to the
// one SQLite file raced the write lock, and with no busy_timeout the loser
// failed instantly with SQLITE_BUSY — but only when the whole suite's load
// stretched a transaction long enough for the overlap. The store, fs, and
// proc all pin SetMaxOpenConns(1) for exactly this; this test makes the
// overlap deliberate instead of load-dependent so the missing discipline
// fails every run, not one run in twenty.
func TestConcurrentWritesNeverBusy(t *testing.T) {
	ctx := context.Background()
	db, _ := openTestDB(t)

	const workers = 8
	errs := make(chan error, workers)
	for w := 0; w < workers; w++ {
		go func() {
			for i := 0; i < 25; i++ {
				c, err := db.Create(ctx, int64(i), 0, 1, 1, "hammer")
				if err != nil {
					errs <- err
					return
				}
				if _, err := db.SetFraming(ctx, c.ID, 1, 2, 0.5); err != nil {
					errs <- err
					return
				}
			}
			errs <- nil
		}()
	}
	for w := 0; w < workers; w++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent write failed: %v", err)
		}
	}
}
