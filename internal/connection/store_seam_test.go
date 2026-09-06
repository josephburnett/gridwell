package connection

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/josephburnett/gridwell/internal/local/store"
)

// This file is the seam between the two owners of the connections table: the
// store owns its shape — the DDL is rendered from the column descriptor and
// migrated with every other node fact — and this package owns the queries over
// it. A unit test on either side alone cannot catch a mismatch, because the
// store's fixtures write SQL of their own and these queries would pass against
// any table that happened to exist. So the real store makes the file and the
// real queries run over it.

// TestEveryQueryRunsOnTheStoresTable exercises each of the connection store's
// queries against a file store.Open made, and reopens the file to prove the
// rows are durable node facts and not something this package materialized.
func TestEveryQueryRunsOnTheStoresTable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "gridwell.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	db, err := NewDB(st.SQL())
	if err != nil {
		t.Fatalf("bind the connection store to a real node store: %v", err)
	}

	if _, err := db.Get(ctx, "rtb"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get on an undeclared name = %v, want ErrNotFound", err)
	}
	if err := db.Ensure(ctx, "rtb"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if err := db.Ensure(ctx, "rtb"); err != nil {
		t.Fatalf("Ensure is not idempotent on the store's table: %v", err)
	}
	if err := db.SetRemoteRoot(ctx, "rtb", "rnode1/7"); err != nil {
		t.Fatalf("SetRemoteRoot: %v", err)
	}
	if err := db.Tombstone(ctx, "olddead"); err != nil {
		t.Fatalf("Tombstone: %v", err)
	}
	if err := db.Revive(ctx, "olddead"); err != nil {
		t.Fatalf("Revive: %v", err)
	}
	if err := db.Tombstone(ctx, "olddead"); err != nil {
		t.Fatalf("Tombstone again: %v", err)
	}
	want := []Stored{{Name: "olddead", Deleted: true}, {Name: "rtb", RemoteRoot: "rnode1/7"}}
	got, err := db.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("List = %+v, want %+v", got, want)
	}

	// The rows are the node's, so they are still there on the next boot.
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	st2, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen the node store: %v", err)
	}
	t.Cleanup(func() { _ = st2.Close() })
	db2, err := NewDB(st2.SQL())
	if err != nil {
		t.Fatal(err)
	}
	got, err = db2.List(ctx)
	if err != nil {
		t.Fatalf("List after reopen: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("List after reopen = %+v, want %+v", got, want)
	}
}

// TestNewDBRefusesAHandleTheStoreNeverOpened pins the ordering this package
// depends on. It no longer creates the table, so a handle the store never
// migrated has none; saying so at the wiring is the difference between a named
// boot failure and every connection read failing later for a reason that does
// not name the cause.
func TestNewDBRefusesAHandleTheStoreNeverOpened(t *testing.T) {
	raw, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "bare.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	raw.SetMaxOpenConns(1)
	if _, err := NewDB(raw); err == nil {
		t.Fatal("NewDB accepted a handle with no connections table")
	}
}
