package cache

import (
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"

	"github.com/josephburnett/gridwell/api/rpc"
)

func changedEvent(n *gridwellv1.Tile) *gridwellv1.Event {
	return &gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{TileChanged: &gridwellv1.TileChanged{Tile: n}}}
}

// A body is bound to the blob it was filed under, not to whichever row the
// cache last held: a pane layout write changes only the blob, and a row can
// reach the cache through a grid that held no earlier row to compare with.
func TestABodyIsBoundToItsOwnBlob(t *testing.T) {
	pane := func(grid string, blob int64) *gridwellv1.Tile {
		return &gridwellv1.Tile{Id: "10", GridId: grid, Kind: rpc.KindPane, Version: 1, BlobId: blob}
	}
	fetchA := func(c *Cache) { c.PutFetchedContent("10", []byte("A"), 1, c.AskContent("10")) }
	cases := []struct {
		name string
		// seed leaves layout A cached, filed under whatever the cache knew.
		seed func(c *Cache)
		// then brings a row naming blob 8 in.
		then func(c *Cache)
	}{
		{"a grid first fetched after the write",
			fetchA,
			func(c *Cache) { c.PutGrid(&gridwellv1.Grid{Id: "1"}, []*gridwellv1.Tile{pane("1", 8)}) }},
		{"a grid that held no earlier row",
			func(c *Cache) {
				c.PutGrid(&gridwellv1.Grid{Id: "2"}, []*gridwellv1.Tile{pane("2", 7)})
				fetchA(c)
			},
			func(c *Cache) { c.PutGrid(&gridwellv1.Grid{Id: "1"}, []*gridwellv1.Tile{pane("1", 8)}) }},
		{"an event for a grid not cached",
			func(c *Cache) {
				c.PutGrid(&gridwellv1.Grid{Id: "2"}, []*gridwellv1.Tile{pane("2", 7)})
				fetchA(c)
			},
			func(c *Cache) { c.Apply(changedEvent(pane("9", 8))) }},
		{"a response for a grid not cached",
			func(c *Cache) {
				c.PutGrid(&gridwellv1.Grid{Id: "2"}, []*gridwellv1.Tile{pane("2", 7)})
				fetchA(c)
			},
			func(c *Cache) { c.UpdateTile("9", pane("9", 8)) }},
		{"the row moved while the read was in flight",
			func(c *Cache) { c.PutGrid(&gridwellv1.Grid{Id: "1"}, []*gridwellv1.Tile{pane("1", 7)}) },
			func(c *Cache) {
				asked := c.AskContent("10")
				c.Apply(changedEvent(pane("1", 8)))
				c.PutFetchedContent("10", []byte("A"), 1, asked)
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := New()
			tc.seed(c)
			tc.then(c)
			if b, ok := c.TileContent("10"); ok {
				t.Fatalf("layout %q answered for a row naming blob 8", b)
			}
			c.PutFetchedContent("10", []byte("B"), 1, c.AskContent("10"))
			if b, ok := c.TileContent("10"); !ok || string(b) != "B" {
				t.Fatalf("a read filed under the current row = %q %v, want B", b, ok)
			}
		})
	}
}

// A body this client just wrote is filed under the response row, so the
// preview reads it at once and the echo of the same row keeps it.
func TestASavedBodyAnswersForItsResponseRow(t *testing.T) {
	c := New()
	c.PutGrid(&gridwellv1.Grid{Id: "1"}, []*gridwellv1.Tile{
		{Id: "10", GridId: "1", Kind: rpc.KindPane, Version: 1, BlobId: 7},
	})
	c.PutFetchedContent("10", []byte("A"), 1, c.AskContent("10"))

	row := &gridwellv1.Tile{Id: "10", GridId: "1", Kind: rpc.KindPane, Version: 1, BlobId: 8}
	c.PutSavedContent(row, []byte("B"))
	c.Apply(changedEvent(row))
	if b, ok := c.TileContent("10"); !ok || string(b) != "B" {
		t.Fatalf("own save = %q %v, want B without a refetch", b, ok)
	}
}

// Unsaved words survive a foreign row on every door, the grid-less ones
// included.
func TestDirtyTextSurvivesAForeignRowAnywhere(t *testing.T) {
	c := New()
	c.PutGrid(&gridwellv1.Grid{Id: "1"}, []*gridwellv1.Tile{
		{Id: "10", GridId: "1", Kind: rpc.KindText, Version: 3, BlobId: 7},
	})
	c.PutFetchedContent("10", []byte("# saved"), 3, c.AskContent("10"))
	c.PutEditedContent("10", []byte("# typing"))

	foreign := &gridwellv1.Tile{Id: "10", GridId: "9", Kind: rpc.KindText, Version: 4, BlobId: 8}
	c.Apply(changedEvent(foreign))
	c.UpdateTile("9", foreign)
	c.PutGrid(&gridwellv1.Grid{Id: "5"}, []*gridwellv1.Tile{foreign})
	if b, ok := c.DirtyContent("10"); !ok || string(b) != "# typing" {
		t.Fatalf("a foreign row discarded unsaved typing: %q %v", b, ok)
	}
	if base, _ := c.SaveBasis("10"); base != 3 {
		t.Errorf("save basis = %d, want 3 so the save conflicts visibly", base)
	}
}

// A tile row has one home: the grid that last answered for it. A move leaves
// its old grid's copy behind until that grid is read again, and a copy left
// behind must not answer for the row, however the grid map iterates.
func TestARowHasOneHomeInTheCache(t *testing.T) {
	row := func(grid string, blob int64) *gridwellv1.Tile {
		return &gridwellv1.Tile{Id: "10", GridId: grid, Kind: rpc.KindPane, Version: 1, BlobId: blob}
	}
	for i := 0; i < 50; i++ {
		c := New()
		c.PutGrid(&gridwellv1.Grid{Id: "2"}, []*gridwellv1.Tile{row("2", 7)})
		c.PutGrid(&gridwellv1.Grid{Id: "1"}, []*gridwellv1.Tile{row("1", 8)})
		if g, _ := c.Grid("2"); len(g.Tiles) != 0 {
			t.Fatalf("the copy a move left in grid 2 is still there")
		}
		if r := c.rowLocked("10"); r == nil || r.BlobId != 8 {
			t.Fatalf("the row answers from %v, want the grid that last answered (blob 8)", r)
		}
		c.Apply(changedEvent(row("3", 9)))
		if g, _ := c.Grid("1"); len(g.Tiles) != 0 {
			t.Fatalf("an event moving the row to an uncached grid left the copy in grid 1")
		}
	}
}

// A plugin row carries no claim, version 0 and no blob, so its event is the
// only news that its bytes moved: a TileChanged that says content_changed
// drops the clean body it names, a dirty one stays for its save to reconcile,
// and a framing event, a client's own patch or a refetch of the same row ages
// nothing.
func TestAClaimlessBodyAgesOnlyWhenAnEventSaysSo(t *testing.T) {
	row := func() *gridwellv1.Tile {
		return &gridwellv1.Tile{Id: "p/~a", GridId: "p/~", Kind: rpc.KindText}
	}
	told := func(n *gridwellv1.Tile) *gridwellv1.Event {
		return &gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{
			TileChanged: &gridwellv1.TileChanged{Tile: n, ContentChanged: true}}}
	}
	seeded := func() *Cache {
		c := New()
		c.PutGrid(&gridwellv1.Grid{Id: "p/~"}, []*gridwellv1.Tile{row()})
		c.PutFetchedContent("p/~a", []byte("one"), 0, c.AskContent("p/~a"))
		return c
	}
	kept := []struct {
		name string
		then func(c *Cache)
	}{
		{"a refetch of the same row", func(c *Cache) { c.PutGrid(&gridwellv1.Grid{Id: "p/~"}, []*gridwellv1.Tile{row()}) }},
		{"a framing event", func(c *Cache) {
			n := row()
			n.TextY = 40
			c.Apply(changedEvent(n))
		}},
		{"the client's own patch", func(c *Cache) { c.PatchTile(row(), func(n *gridwellv1.Tile) { n.TextY = 40 }) }},
		{"a write response", func(c *Cache) { c.UpdateTile("p/~", row()) }},
	}
	for _, tc := range kept {
		t.Run(tc.name, func(t *testing.T) {
			c := seeded()
			tc.then(c)
			if b, ok := c.TileContent("p/~a"); !ok || string(b) != "one" {
				t.Errorf("the body after %s = %q, %v; want it kept", tc.name, b, ok)
			}
		})
	}
	t.Run("an event that says the bytes moved", func(t *testing.T) {
		c := seeded()
		c.Apply(told(row()))
		if b, ok := c.TileContent("p/~a"); ok {
			t.Errorf("the body after content_changed = %q; want it dropped for a refetch", b)
		}
	})
	t.Run("an event that says so for a grid not cached", func(t *testing.T) {
		c := seeded()
		n := row()
		n.GridId = "p/~elsewhere"
		c.Apply(told(n))
		if _, ok := c.TileContent("p/~a"); ok {
			t.Error("the body survived content_changed arriving for a grid the cache does not hold")
		}
	})
	t.Run("a dirty body", func(t *testing.T) {
		c := seeded()
		c.PutEditedContent("p/~a", []byte("typed"))
		c.Apply(told(row()))
		if b, ok := c.TileContent("p/~a"); !ok || string(b) != "typed" {
			t.Errorf("the dirty body after content_changed = %q, %v; want it kept", b, ok)
		}
	})
}

// A body's stamp names the bytes the server last gave, not the row: a plugin
// body read again after its event said it moved gets a new stamp at the same
// version, so a picture or wrap keyed by it is made again; typing keeps it,
// as typing keeps the version; a save gives the saved bytes their own.
func TestAStampNamesTheBytesNotTheRow(t *testing.T) {
	c := New()
	row := &gridwellv1.Tile{Id: "p/~a", GridId: "p/~", Kind: rpc.KindText}
	c.PutGrid(&gridwellv1.Grid{Id: "p/~"}, []*gridwellv1.Tile{row})
	if s := c.ContentStamp("p/~a"); s != 0 {
		t.Fatalf("no body, stamp %d; want 0", s)
	}
	c.PutFetchedContent("p/~a", []byte("one"), 0, c.AskContent("p/~a"))
	first := c.ContentStamp("p/~a")
	if first == 0 {
		t.Fatal("a fetched body has no stamp")
	}
	c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{
		TileChanged: &gridwellv1.TileChanged{Tile: row, ContentChanged: true}}})
	if s := c.ContentStamp("p/~a"); s != 0 {
		t.Fatalf("an aged body still stamped %d", s)
	}
	c.PutFetchedContent("p/~a", []byte("two"), 0, c.AskContent("p/~a"))
	second := c.ContentStamp("p/~a")
	if second == 0 || second == first {
		t.Fatalf("new bytes at the same version stamped %d after %d; want a new stamp", second, first)
	}
	c.PutEditedContent("p/~a", []byte("two, typed"))
	if s := c.ContentStamp("p/~a"); s != second {
		t.Errorf("typing moved the stamp %d -> %d", second, s)
	}
	c.PutSavedContent(&gridwellv1.Tile{Id: "p/~a", Version: 1}, []byte("two, typed"))
	if s := c.ContentStamp("p/~a"); s == second || s == 0 {
		t.Errorf("saved bytes kept stamp %d", s)
	}
}
