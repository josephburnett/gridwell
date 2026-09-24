package store

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/internal/trace"
)

// find is the newest record whose msg is exactly want.
func find(records []trace.Record, want string) (trace.Record, bool) {
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].Msg == want {
			return records[i], true
		}
	}
	return trace.Record{}, false
}

// Every store write says what it did, from the one funnel it already goes
// through: what it touched is read off the events it is about to publish, so a
// new mutating method is traced without a line of its own.
func TestAStoreWriteSaysWhatItTouched(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root, err := s.RootGridID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tile, err := s.CreateText(ctx, root, 0, 0, 2, 2, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := find(trace.Default().Snapshot(), "CreateTile")
	if !ok {
		t.Fatal("a create left no record")
	}
	if rec.Src != "store" || rec.Kind != "write" {
		t.Errorf("record = %+v, want the store's write", rec)
	}
	if !strings.Contains(rec.KV["keys"], tile.Id) {
		t.Errorf("keys %q does not name the created tile %s", rec.KV["keys"], tile.Id)
	}
	if rec.KV["kind"] != tile.Kind || rec.KV["v"] != strconv.FormatInt(tile.Version, 10) {
		t.Errorf("record kv = %v, want the tile's kind %q at version %d", rec.KV, tile.Kind, tile.Version)
	}
}

// A write that rolls back is the interesting one: nothing changed, and the
// record is the only thing that says the user asked.
func TestARefusedStoreWriteSaysSo(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root, err := s.RootGridID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateText(ctx, root, 0, 0, 2, 2, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateText(ctx, root, 0, 0, 2, 2, []byte("overlapping")); err == nil {
		t.Fatal("an overlapping create was accepted")
	}
	for _, rec := range trace.Default().Snapshot() {
		if rec.Src == "store" && strings.HasPrefix(rec.Msg, "CreateTile error:") {
			return
		}
	}
	t.Error("a refused write left no record")
}
