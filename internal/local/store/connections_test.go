package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/internal/trace"
)

// Every connection-row query runs on a file Open made, and the rows are still
// there after a reopen: they are durable node facts.
func TestConnectionRowsAreDurableNodeFacts(t *testing.T) {
	ctx := context.Background()
	s, path := newTestStoreFile(t)
	if _, err := s.Connection(ctx, "rtb"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Connection on an undeclared name = %v, want ErrNotFound", err)
	}
	for _, w := range []func() error{
		func() error { return s.DeclareConnection(ctx, "rtb") },
		func() error { return s.DeclareConnection(ctx, "rtb") },
		func() error { return s.SetConnectionRoot(ctx, "rtb", "rnode1/7") },
		func() error { return s.SetConnectionRetired(ctx, "olddead", true) },
		func() error { return s.SetConnectionRetired(ctx, "olddead", false) },
		func() error { return s.SetConnectionRetired(ctx, "olddead", true) },
	} {
		if err := w(); err != nil {
			t.Fatal(err)
		}
	}
	want := []ConnectionRow{{Name: "olddead", Deleted: true}, {Name: "rtb", RemoteRoot: "rnode1/7"}}
	if got, err := s.Connections(ctx); err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("Connections = %+v, %v; want %+v", got, err, want)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	if got, err := s2.Connections(ctx); err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("Connections after reopen = %+v, %v; want %+v", got, err, want)
	}
}

// A connection row is a node fact like a tile, so its write goes through the
// one funnel and leaves the same record, naming the row.
func TestAConnectionRowWriteSaysWhatItTouched(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	for _, w := range []struct {
		verb  string
		write func() error
	}{
		{"DeclareConnection", func() error { return s.DeclareConnection(ctx, "rtb") }},
		{"SetConnectionRoot", func() error { return s.SetConnectionRoot(ctx, "rtb", "rnode1/7") }},
		{"RetireConnection", func() error { return s.SetConnectionRetired(ctx, "rtb", true) }},
		{"ReviveConnection", func() error { return s.SetConnectionRetired(ctx, "rtb", false) }},
	} {
		if err := w.write(); err != nil {
			t.Fatalf("%s: %v", w.verb, err)
		}
		rec, ok := find(trace.Default().Snapshot(), w.verb)
		if !ok {
			t.Errorf("%s left no record", w.verb)
			continue
		}
		if rec.Src != "store" || rec.Kind != "write" || rec.KV["keys"] != "conn/rtb" {
			t.Errorf("%s record = %+v, want the store's write naming conn/rtb", w.verb, rec)
		}
	}
}

// A refused connection-row write says so, as every other store write does.
func TestARefusedConnectionRowWriteSaysSo(t *testing.T) {
	s := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.DeclareConnection(ctx, "gone"); err == nil {
		t.Fatal("a write under a cancelled context succeeded")
	}
	for _, rec := range trace.Default().Snapshot() {
		if rec.Src == "store" && strings.HasPrefix(rec.Msg, "DeclareConnection error:") {
			return
		}
	}
	t.Error("a refused connection-row write left no record")
}
