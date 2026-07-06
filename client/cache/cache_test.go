package cache

import (
	"testing"

	"github.com/josephburnett/gridwell/internal/rpc"
)

func seedCache(t *testing.T) *Cache {
	t.Helper()
	c := New()
	c.PutGrid(rpc.Grid{ID: "1"}, []rpc.Tile{
		{ID: "100", GridID: "1", Kind: rpc.KindWell, X: 0, Y: 0, W: 1, H: 1, ChildGridID: "2"},
		{ID: "101", GridID: "1", Kind: rpc.KindText, X: 5, Y: 5, W: 1, H: 1},
	})
	return c
}

func TestPutAndGet(t *testing.T) {
	c := seedCache(t)
	g, ok := c.Grid("1")
	if !ok || g == nil {
		t.Fatal("missing grid")
	}
	if len(g.Tiles) != 2 {
		t.Errorf("nodes = %d", len(g.Tiles))
	}
	// Mutating the snapshot must not affect the cache.
	delete(g.Tiles, "100")
	if g2, _ := c.Grid("1"); len(g2.Tiles) != 2 {
		t.Errorf("snapshot wasn't deep enough")
	}
}

// TestTileContentEditDoesNotLeakToClone is the regression for the cross-clone
// content leak: two cloned text tiles have distinct ids. An edit to one writes
// its body keyed by that id, so the sibling's body is untouched (the "edit one
// clone, the other changed too" bug). Tile-id keying makes this hold by
// construction — the blob-id store that once needed careful repointing is gone.
func TestTileContentEditDoesNotLeakToClone(t *testing.T) {
	c := New()
	// Clones: distinct tile rows in distinct grids, both seeded with the same
	// body — but each addressed by its own tile id.
	c.PutGrid(rpc.Grid{ID: "1"}, []rpc.Tile{{ID: "10", GridID: "1", Kind: rpc.KindText}})
	c.PutGrid(rpc.Grid{ID: "2"}, []rpc.Tile{{ID: "20", GridID: "2", Kind: rpc.KindText}})
	c.PutTileContent("10", []byte("Hello World"))
	c.PutTileContent("20", []byte("Hello World"))

	c.PutTileContent("10", []byte("Goodbye"))

	if b, _ := c.TileContent("10"); string(b) != "Goodbye" {
		t.Errorf("edited tile body = %q, want Goodbye", b)
	}
	if b, _ := c.TileContent("20"); string(b) != "Hello World" {
		t.Errorf("sibling body = %q, want Hello World (edit leaked)", b)
	}
}

// TestRenderedEditVisibleThroughRenderAccessor reproduces the rendered-mode
// "typing does nothing" bug: the renderer reads a text tile's body via
// TileContent (tileBody -> TileContent, the tile-id content store), but the
// edit path wrote through OptimisticEdit (the blob-id store). The two stores
// disagree, so edits never appear. The fix routes edits through the same store
// the renderer reads.
func TestRenderedEditVisibleThroughRenderAccessor(t *testing.T) {
	c := New()
	c.PutGrid(rpc.Grid{ID: "1"}, []rpc.Tile{{ID: "10", GridID: "1", Kind: rpc.KindText}})
	c.PutTileContent("10", []byte("Hello")) // what the renderer reads

	c.PutTileContent("10", []byte("Hello world")) // what an edit writes

	got, _ := c.TileContent("10")
	if string(got) != "Hello world" {
		t.Fatalf("TileContent = %q, want %q (edit invisible to the renderer)", got, "Hello world")
	}
}

func TestApplyTileChanged(t *testing.T) {
	c := seedCache(t)
	ok := c.Apply(rpc.Event{
		Kind:        rpc.EventTileChanged,
		TileChanged: &rpc.TileChanged{Tile: rpc.Tile{ID: "100", GridID: "1", Kind: rpc.KindWell, X: 9, Y: 9, W: 2, H: 2, ChildGridID: "2"}},
	})
	if !ok {
		t.Error("Apply returned false")
	}
	g, _ := c.Grid("1")
	if g.Tiles["100"].W != 2 {
		t.Errorf("tile not updated: %+v", g.Tiles["100"])
	}
}

// TestApplyStaleEchoDropped: the optimistic-echo interlock (I11 residual,
// issue #5). After a local mutation's response lands in the cache (version
// N), a still-in-flight Subscribe echo of the PREVIOUS state (N-1) must be
// dropped — applying it would visibly roll the tile back and forward, i.e.
// mutation the user never made. Same-version events (framing — never bumps
// version) and newer events still apply.
func TestApplyStaleEchoDropped(t *testing.T) {
	c := seedCache(t)
	// The mutation response landed: version 5.
	c.UpdateTile("1", rpc.Tile{ID: "100", GridID: "1", Kind: rpc.KindText, Version: 5, X: 1})

	// A stale echo (version 4) arrives late: dropped, no visible change.
	if c.Apply(rpc.Event{Kind: rpc.EventTileChanged,
		TileChanged: &rpc.TileChanged{Tile: rpc.Tile{ID: "100", GridID: "1", Kind: rpc.KindText, Version: 4, X: 99}}}) {
		t.Error("stale echo applied — the tile would roll back")
	}
	g, _ := c.Grid("1")
	if g.Tiles["100"].X != 1 || g.Tiles["100"].Version != 5 {
		t.Errorf("tile after stale echo = %+v, want the newer row untouched", g.Tiles["100"])
	}

	// A SAME-version event applies: framing writes change view_* without a
	// version bump, and dropping them would freeze pans.
	if !c.Apply(rpc.Event{Kind: rpc.EventTileChanged,
		TileChanged: &rpc.TileChanged{Tile: rpc.Tile{ID: "100", GridID: "1", Kind: rpc.KindText, Version: 5, X: 2}}}) {
		t.Error("same-version event dropped — framing echoes would freeze")
	}
	// And a newer one, obviously.
	if !c.Apply(rpc.Event{Kind: rpc.EventTileChanged,
		TileChanged: &rpc.TileChanged{Tile: rpc.Tile{ID: "100", GridID: "1", Kind: rpc.KindText, Version: 6, X: 3}}}) {
		t.Error("newer event dropped")
	}
	g, _ = c.Grid("1")
	if g.Tiles["100"].X != 3 {
		t.Errorf("final tile = %+v, want the newest row", g.Tiles["100"])
	}
}

func TestApplyTileRemoved(t *testing.T) {
	c := seedCache(t)
	ok := c.Apply(rpc.Event{
		Kind:        rpc.EventTileRemoved,
		TileRemoved: &rpc.TileRemoved{GridID: "1", TileID: "100"},
	})
	if !ok {
		t.Error("Apply returned false")
	}
	g, _ := c.Grid("1")
	if _, ok := g.Tiles["100"]; ok {
		t.Error("tile still present")
	}
	// Idempotent: removing again returns false (nothing changed).
	if c.Apply(rpc.Event{
		Kind:        rpc.EventTileRemoved,
		TileRemoved: &rpc.TileRemoved{GridID: "1", TileID: "100"},
	}) {
		t.Error("expected false on second remove")
	}
}

func TestApplyEventForUnknownGridIgnored(t *testing.T) {
	c := seedCache(t)
	ok := c.Apply(rpc.Event{
		Kind:        rpc.EventTileChanged,
		TileChanged: &rpc.TileChanged{Tile: rpc.Tile{ID: "999", GridID: "999", Kind: rpc.KindWell, ChildGridID: "1"}},
	})
	if ok {
		t.Error("expected false for unknown grid")
	}
}

func TestTileContentPutGet(t *testing.T) {
	c := New()
	if _, ok := c.TileContent("7"); ok {
		t.Fatal("empty cache should not have content for tile 7")
	}
	c.PutTileContent("7", []byte("hello"))
	b, ok := c.TileContent("7")
	if !ok {
		t.Fatal("tile 7 content missing after put")
	}
	if string(b) != "hello" {
		t.Errorf("content = %q, want hello", string(b))
	}
	// PutTileContent copies the bytes so caller mutations don't propagate.
	src := []byte("world")
	c.PutTileContent("8", src)
	src[0] = 'X'
	got, _ := c.TileContent("8")
	if string(got) != "world" {
		t.Errorf("after mutating source, tile 8 content = %q (cache should hold its own copy)", string(got))
	}
}

func TestDropTileContent(t *testing.T) {
	c := New()
	c.PutTileContent("7", []byte("rejected optimistic edit"))
	c.DropTileContent("7")
	if _, ok := c.TileContent("7"); ok {
		t.Fatal("content survived DropTileContent; a rejected edit would keep rendering as saved")
	}
	c.DropTileContent("absent") // no-op
}

func TestKnownGridIDs(t *testing.T) {
	c := New()
	c.PutGrid(rpc.Grid{ID: "1"}, []rpc.Tile{})
	c.PutGrid(rpc.Grid{ID: "2"}, []rpc.Tile{})

	got := c.KnownGridIDs()
	if len(got) != 2 {
		t.Errorf("len = %d, want 2", len(got))
	}
	have := map[string]bool{}
	for _, id := range got {
		have[id] = true
	}
	if !have["1"] || !have["2"] {
		t.Errorf("KnownGridIDs missing entries: %v", got)
	}
}

func TestUpdateTile(t *testing.T) {
	c := New()
	c.PutGrid(rpc.Grid{ID: "10"}, []rpc.Tile{
		{ID: "100", GridID: "10", Kind: rpc.KindURL, X: 0, Y: 0, W: 1, H: 1},
	})

	// Update the existing tile: change W.
	updated := rpc.Tile{ID: "100", GridID: "10", Kind: rpc.KindURL, X: 0, Y: 0, W: 3, H: 1}
	c.UpdateTile("10", updated)

	g, _ := c.Grid("10")
	if g.Tiles["100"].W != 3 {
		t.Errorf("UpdateTile did not change W; got %d", g.Tiles["100"].W)
	}

	// UpdateTile on an unknown grid is a no-op.
	c.UpdateTile("999", updated)

	// UpdateTile on an unknown tile id within a known grid is a no-op.
	stranger := rpc.Tile{ID: "999", GridID: "10", Kind: rpc.KindText}
	c.UpdateTile("10", stranger)
	if _, ok := g.Tiles["999"]; ok {
		t.Error("UpdateTile should not insert unknown tile ids")
	}
}

// TestRemoveTileFreesContent is the regression for a body leak: editing a text
// tile stores its body in the content map keyed by tile id. If the tile is then
// removed (e.g. dragged onto the + trashcan), its cached body must be dropped —
// otherwise it strands in the map forever.
func TestRemoveTileFreesContent(t *testing.T) {
	c := New()
	c.PutGrid(rpc.Grid{ID: "1"}, []rpc.Tile{{ID: "10", GridID: "1", Kind: rpc.KindText}})
	c.PutTileContent("10", []byte("Goodbye"))
	if _, ok := c.TileContent("10"); !ok {
		t.Fatal("content not stored")
	}

	c.Apply(rpc.Event{Kind: rpc.EventTileRemoved, TileRemoved: &rpc.TileRemoved{GridID: "1", TileID: "10"}})
	if _, ok := c.TileContent("10"); ok {
		t.Errorf("content leaked after tile removal")
	}
}
