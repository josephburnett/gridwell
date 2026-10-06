package store

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/api/tracewire"
	"github.com/josephburnett/gridwell/internal/trace"
)

// find is the newest record whose msg is exactly want.
func find(records []tracewire.Record, want string) (tracewire.Record, bool) {
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].Msg == want {
			return records[i], true
		}
	}
	return tracewire.Record{}, false
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

// A plugin row's write goes through the same funnel as home's, so it leaves
// the same record. It publishes no event to read its row off, so it names
// the row itself, qualified by its namespace.
func TestAPluginRowWriteSaysWhatItTouched(t *testing.T) {
	_, d := openExt(t)
	gid, err := d.ContextID("inbox")
	if err != nil {
		t.Fatal(err)
	}
	gs := strconv.FormatInt(gid, 10)
	id, err := d.Mint(gid, &pluginv1.Entry{Key: "a", Kind: "text", Label: "a"}, 0, 0, 0, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	ts := strconv.FormatInt(id, 10)
	f, err := rpc.NewFraming(1, 2, 1.5)
	if err != nil {
		t.Fatal(err)
	}
	z, err := rpc.NewContentZoom(1.25)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []struct {
		verb, keys string
		write      func() error
	}{
		{"Place", "plug1:t/" + ts, func() error { return d.Place(id, 3, 3, 1, 1) }},
		{"SetFraming", "plug1:t/" + ts, func() error { return d.SetFraming(id, 0, f) }},
		{"SetFraming", "plug1:f/" + gs, func() error { return d.SetFraming(0, gid, f) }},
		{"SetTextView", "plug1:t/" + ts, func() error { return d.SetTextView(id, 0, 0, 4, 4, "") }},
		{"SetContentZoom", "plug1:t/" + ts, func() error { return d.SetContentZoom(id, z) }},
		{"Mint", "plug1:g/" + gs + "/b", func() error {
			_, err := d.Mint(gid, &pluginv1.Entry{Key: "b", Kind: "text", Label: "b"}, 0, 5, 5, 1, 1)
			return err
		}},
		{"ContextID", "plug1:c/sent", func() error { _, err := d.ContextID("sent"); return err }},
	} {
		if err := w.write(); err != nil {
			t.Fatalf("%s: %v", w.verb, err)
		}
		rec, ok := find(trace.Default().Snapshot(), w.verb)
		if !ok {
			t.Errorf("%s on a plugin row left no record", w.verb)
			continue
		}
		if rec.Src != "store" || rec.Kind != "write" || rec.KV["keys"] != w.keys {
			t.Errorf("%s record = %+v, want the store's write naming %q", w.verb, rec, w.keys)
		}
	}
}

// A refused plugin-row write says so, as a refused home write does.
func TestARefusedPluginRowWriteSaysSo(t *testing.T) {
	_, d := openExt(t)
	if err := d.Place(999999, 0, 0, 1, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Place on no row = %v, want ErrNotFound", err)
	}
	for _, rec := range trace.Default().Snapshot() {
		if rec.Src == "store" && strings.HasPrefix(rec.Msg, "Place error:") {
			return
		}
	}
	t.Error("a refused plugin-row write left no record")
}
