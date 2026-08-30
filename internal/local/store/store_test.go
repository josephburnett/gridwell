package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/josephburnett/gridwell/api/rpc"
)

// seedDeterministic pins a fixed clock and counter-based ids so tests are
// reproducible. Shared by the in-memory and file-backed constructors.
func seedDeterministic(s *Store) {
	fixed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return fixed })
	var counter int
	s.SetIDGenerator(func() string {
		counter++
		return fmt.Sprintf("obj-%028x", counter)
	})
}

// newTestStore opens an in-memory store and seeds deterministic clocks/IDs so
// tests are reproducible. Each test gets a fresh store with a bootstrapped
// root grid.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	seedDeterministic(s)
	return s
}

// newTestStoreFile opens a file-backed store under t.TempDir and returns it
// with its path so a test can Close and reopen the same file. File-backed
// because a ":memory:" DB forces journal_mode=memory, so WAL and the pinned
// synchronous level — the durability we most need to prove — are inert there.
func newTestStoreFile(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	seedDeterministic(s)
	return s, path
}

// rootID returns the bootstrapped root grid id as a string.
func rootID(t *testing.T, s *Store) string {
	t.Helper()
	id, err := s.RootGridID(context.Background())
	if err != nil {
		t.Fatalf("root grid id: %v", err)
	}
	return id
}

func TestOpenAppliesSchema(t *testing.T) {
	s := newTestStore(t)
	rows, err := s.db.Query(
		`SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		got = append(got, n)
	}
	want := map[string]bool{
		"grids": true, "tiles": true, "blobs": true, "system": true,
	}
	for _, g := range got {
		delete(want, g)
	}
	if len(want) > 0 {
		t.Errorf("missing tables: %v (have %v)", want, got)
	}
}

func TestBootstrapRoot(t *testing.T) {
	s := newTestStore(t)
	id, err := s.RootGridID(context.Background())
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	if id == "" {
		t.Error("root id is empty")
	}
}

// TestRootFramingRoundTrip writes home's root framing through the ONE
// writer (SetFraming aimed at the root GRID row — schema v11 put it in
// the same three columns every other root uses) and reads it back
// EXACTLY: a float column keeps the float, so nothing rounds on the way
// through the store.
func TestRootFramingRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, ok, err := s.RootFraming(ctx); err != nil || ok {
		t.Fatalf("a fresh home claims a root framing: ok=%v err=%v", ok, err)
	}
	root, err := s.RootGridID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := rpc.Framing{Cx: 3.5, Cy: -7.25, Zoom: 1.5}
	if _, err := s.SetFraming(ctx, &rpc.SetFramingRequest{RootGridID: root, Framing: want}); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, ok, err := s.RootFraming(ctx)
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if got != want {
		t.Errorf("RootFraming = %+v, want %+v", got, want)
	}
}
