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
		{ID: "101", GridID: "1", Kind: rpc.KindText, X: 5, Y: 5, W: 1, H: 1, BlobID: 1},
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

// TestOptimisticEditDoesNotLeakToClone is the regression for the cross-clone
// content leak: two cloned text tiles (different ids, different grids) share
// one content-addressed blob id. Optimistically editing one must NOT touch the
// blob the other still points at, or the sibling renders — and then persists —
// the wrong content (the "edit one clone, the other changed too" bug).
func TestOptimisticEditDoesNotLeakToClone(t *testing.T) {
	c := New()
	// gridA/textA and gridB/textB are clones: distinct tile rows, distinct
	// grids, but the same shared blob id 5 ("Hello World").
	c.PutGrid(rpc.Grid{ID: "1"}, []rpc.Tile{{ID: "10", GridID: "1", Kind: rpc.KindText, BlobID: 5}})
	c.PutGrid(rpc.Grid{ID: "2"}, []rpc.Tile{{ID: "20", GridID: "2", Kind: rpc.KindText, BlobID: 5}})
	c.PutBlob(5, []byte("Hello World"))

	if !c.OptimisticEdit("1", "10", []byte("Goodbye")) {
		t.Fatal("OptimisticEdit returned false")
	}

	// The shared blob is untouched.
	if b, _ := c.Blob(5); string(b) != "Hello World" {
		t.Errorf("shared blob 5 = %q, want Hello World (edit leaked)", b)
	}
	// The sibling clone still points at the shared blob and renders the old
	// content.
	gB, _ := c.Grid("2")
	if gB.Tiles["20"].BlobID != 5 {
		t.Errorf("sibling BlobID = %d, want 5 (unchanged)", gB.Tiles["20"].BlobID)
	}
	// The edited tile points at a fresh local (negative) blob with the new
	// content.
	gA, _ := c.Grid("1")
	newID := gA.Tiles["10"].BlobID
	if newID >= 0 {
		t.Errorf("edited BlobID = %d, want a negative optimistic id", newID)
	}
	if b, _ := c.Blob(newID); string(b) != "Goodbye" {
		t.Errorf("optimistic blob = %q, want Goodbye", b)
	}
}

// TestOptimisticEditReconcilesOnTileChanged: once the server's TileChanged
// arrives with the real blob id, the optimistic local blob is dropped.
func TestOptimisticEditReconcilesOnTileChanged(t *testing.T) {
	c := New()
	c.PutGrid(rpc.Grid{ID: "1"}, []rpc.Tile{{ID: "10", GridID: "1", Kind: rpc.KindText, BlobID: 5}})
	c.PutBlob(5, []byte("old"))
	c.OptimisticEdit("1", "10", []byte("new"))
	g, _ := c.Grid("1")
	localID := g.Tiles["10"].BlobID

	c.Apply(rpc.Event{
		Kind:        rpc.EventTileChanged,
		TileChanged: &rpc.TileChanged{Tile: rpc.Tile{ID: "10", GridID: "1", Kind: rpc.KindText, BlobID: 9}},
	})
	if _, ok := c.Blob(localID); ok {
		t.Errorf("optimistic blob %d survived reconciliation", localID)
	}
	g, _ = c.Grid("1")
	if g.Tiles["10"].BlobID != 9 {
		t.Errorf("tile BlobID = %d, want 9 (server id)", g.Tiles["10"].BlobID)
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

func TestBlobPutGetInvalidate(t *testing.T) {
	c := New()
	if _, ok := c.Blob(7); ok {
		t.Fatal("empty cache should not have blob 7")
	}
	c.PutBlob(7, []byte("hello"))
	b, ok := c.Blob(7)
	if !ok {
		t.Fatal("blob 7 missing after put")
	}
	if string(b) != "hello" {
		t.Errorf("blob data = %q, want hello", string(b))
	}
	// PutBlob copies the bytes so caller mutations don't propagate.
	src := []byte("world")
	c.PutBlob(8, src)
	src[0] = 'X'
	got, _ := c.Blob(8)
	if string(got) != "world" {
		t.Errorf("after mutating source, blob 8 = %q (cache should hold its own copy)", string(got))
	}
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

// TestRemoveTileFreesOptimisticBlob is the regression for an optimistic-blob
// leak: editing a text tile stores a client-local (negative-id) blob and
// repoints the tile at it. If the tile is then removed (e.g. dragged onto the
// + trashcan) before the authoritative server tile arrives, the optimistic blob
// must be dropped from the map — otherwise it strands forever, exactly the
// unbounded growth OptimisticEdit and the EventTileChanged reconcile guard
// against.
func TestRemoveTileFreesOptimisticBlob(t *testing.T) {
	c := New()
	c.PutGrid(rpc.Grid{ID: "1"}, []rpc.Tile{{ID: "10", GridID: "1", Kind: rpc.KindText, BlobID: 5}})
	c.PutBlob(5, []byte("Hello"))

	if !c.OptimisticEdit("1", "10", []byte("Goodbye")) {
		t.Fatal("OptimisticEdit returned false")
	}
	// The tile now points at a negative optimistic blob id.
	g, _ := c.Grid("1")
	optID := g.Tiles["10"].BlobID
	if optID >= 0 {
		t.Fatalf("expected negative optimistic blob id, got %d", optID)
	}
	if _, ok := c.Blob(optID); !ok {
		t.Fatal("optimistic blob not stored")
	}

	// Removing the tile must release its optimistic blob.
	c.Apply(rpc.Event{Kind: rpc.EventTileRemoved, TileRemoved: &rpc.TileRemoved{GridID: "1", TileID: "10"}})
	if _, ok := c.Blob(optID); ok {
		t.Errorf("optimistic blob %d leaked after tile removal", optID)
	}
}
