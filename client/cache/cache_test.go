package cache

import (
	"testing"

	"github.com/josephburnett/gridwell/internal/rpc"
)

func seedCache(t *testing.T) *Cache {
	t.Helper()
	c := New()
	c.PutGrid(rpc.Grid{ID: 1}, []rpc.Tile{
		{ID: 100, GridID: 1, Kind: rpc.KindWell, X: 0, Y: 0, W: 1, H: 1, ChildGridID: 2},
		{ID: 101, GridID: 1, Kind: rpc.KindText, X: 5, Y: 5, W: 1, H: 1, BlobID: 1},
	})
	return c
}

func TestPutAndGet(t *testing.T) {
	c := seedCache(t)
	g, ok := c.Grid(1)
	if !ok || g == nil {
		t.Fatal("missing grid")
	}
	if len(g.Tiles) != 2 {
		t.Errorf("nodes = %d", len(g.Tiles))
	}
	// Mutating the snapshot must not affect the cache.
	delete(g.Tiles, 100)
	if g2, _ := c.Grid(1); len(g2.Tiles) != 2 {
		t.Errorf("snapshot wasn't deep enough")
	}
}

func TestApplyTileChanged(t *testing.T) {
	c := seedCache(t)
	ok := c.Apply(rpc.Event{
		Kind:        rpc.EventTileChanged,
		TileChanged: &rpc.TileChanged{Tile: rpc.Tile{ID: 100, GridID: 1, Kind: rpc.KindWell, X: 9, Y: 9, W: 2, H: 2, ChildGridID: 2}},
	})
	if !ok {
		t.Error("Apply returned false")
	}
	g, _ := c.Grid(1)
	if g.Tiles[100].W != 2 {
		t.Errorf("tile not updated: %+v", g.Tiles[100])
	}
}

func TestApplyTileRemoved(t *testing.T) {
	c := seedCache(t)
	ok := c.Apply(rpc.Event{
		Kind:        rpc.EventTileRemoved,
		TileRemoved: &rpc.TileRemoved{GridID: 1, TileID: 100},
	})
	if !ok {
		t.Error("Apply returned false")
	}
	g, _ := c.Grid(1)
	if _, ok := g.Tiles[100]; ok {
		t.Error("tile still present")
	}
	// Idempotent: removing again returns false (nothing changed).
	if c.Apply(rpc.Event{
		Kind:        rpc.EventTileRemoved,
		TileRemoved: &rpc.TileRemoved{GridID: 1, TileID: 100},
	}) {
		t.Error("expected false on second remove")
	}
}

func TestApplyGridForked(t *testing.T) {
	c := seedCache(t)
	ok := c.Apply(rpc.Event{
		Kind:       rpc.EventGridForked,
		GridForked: &rpc.GridForked{WellID: 100, OldGridID: 2, NewGridID: 99},
	})
	if !ok {
		t.Error("Apply returned false")
	}
	g, _ := c.Grid(1)
	if g.Tiles[100].ChildGridID != 99 {
		t.Errorf("well not redirected: %+v", g.Tiles[100])
	}
}

func TestApplyEventForUnknownGridIgnored(t *testing.T) {
	c := seedCache(t)
	ok := c.Apply(rpc.Event{
		Kind:        rpc.EventTileChanged,
		TileChanged: &rpc.TileChanged{Tile: rpc.Tile{ID: 999, GridID: 999, Kind: rpc.KindWell, ChildGridID: 1}},
	})
	if ok {
		t.Error("expected false for unknown grid")
	}
}

func TestKnownWellIDs(t *testing.T) {
	c := seedCache(t)
	known := c.KnownWellIDs()
	if !known[100] {
		t.Errorf("well id 100 missing from known: %v", known)
	}
	if known[101] {
		t.Errorf("text id 101 should not be in known wells: %v", known)
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
	c.PutGrid(rpc.Grid{ID: 1}, []rpc.Tile{})
	c.PutGrid(rpc.Grid{ID: 2}, []rpc.Tile{})

	got := c.KnownGridIDs()
	if len(got) != 2 {
		t.Errorf("len = %d, want 2", len(got))
	}
	have := map[int64]bool{}
	for _, id := range got {
		have[id] = true
	}
	if !have[1] || !have[2] {
		t.Errorf("KnownGridIDs missing entries: %v", got)
	}
}

func TestUpdateTile(t *testing.T) {
	c := New()
	c.PutGrid(rpc.Grid{ID: 10}, []rpc.Tile{
		{ID: 100, GridID: 10, Kind: rpc.KindURL, X: 0, Y: 0, W: 1, H: 1},
	})

	// Update the existing tile: change W.
	updated := rpc.Tile{ID: 100, GridID: 10, Kind: rpc.KindURL, X: 0, Y: 0, W: 3, H: 1}
	c.UpdateTile(10, updated)

	g, _ := c.Grid(10)
	if g.Tiles[100].W != 3 {
		t.Errorf("UpdateTile did not change W; got %d", g.Tiles[100].W)
	}

	// UpdateTile on an unknown grid is a no-op.
	c.UpdateTile(999, updated)

	// UpdateTile on an unknown tile id within a known grid is a no-op.
	stranger := rpc.Tile{ID: 999, GridID: 10, Kind: rpc.KindText}
	c.UpdateTile(10, stranger)
	if _, ok := g.Tiles[999]; ok {
		t.Error("UpdateTile should not insert unknown tile ids")
	}
}
