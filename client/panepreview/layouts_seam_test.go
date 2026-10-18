package panepreview

import (
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/cache"
	"github.com/josephburnett/gridwell/client/pane"
)

// The preview across its seam with the content cache, as the shim wires it:
// a workspace's layout is rewritten without a leaf's tile (an ephemeral
// visit ended and was deleted), and the next preview of that workspace must
// never draw the leaf, since drawing it asks for the gone tile.
func TestAPreviewNeverDrawsAnOlderBlobsLeaves(t *testing.T) {
	encode := func(tr *pane.Tree) []byte {
		data, _, err := pane.EncodeLayout(tr, nil)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	withGone := pane.TreeAtPlace("", "g0", nil, rpc.ViewOf(0, 0, 1))
	withGone.FocusedPane().Stack = pane.StackAt("g0", nil, "gone")
	before, after := encode(withGone), encode(pane.TreeAtPlace("", "g0", nil, rpc.ViewOf(0, 0, 1)))

	row := func(blob int64) *gridwellv1.Tile {
		return &gridwellv1.Tile{Id: "w", GridId: "g", Kind: rpc.KindWorkspace, Version: 1, BlobId: blob}
	}
	c := cache.New()
	c.PutGrid(&gridwellv1.Grid{Id: "g"}, []*gridwellv1.Tile{row(7)})
	l := NewLayouts(nil)
	tree := func(blob int64) (*pane.Tree, bool) {
		return l.Tree("w", blob, c.BodyGen("w"), func() ([]byte, bool) { return c.TileContent("w") })
	}
	names := func(tr *pane.Tree) bool {
		found := false
		tr.Walk(func(p *pane.Pane) { found = found || p.ContentID() == "gone" })
		return found
	}

	c.PutFetchedContent("w", before, rpc.ContentBasis{Version: 1}, c.AskContent("w"))
	if tr, ok := tree(7); !ok || !names(tr) {
		t.Fatal("the first layout should decode with its leaf")
	}

	c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{TileChanged: &gridwellv1.TileChanged{Tile: row(8)}}})
	if tr, ok := tree(8); ok && names(tr) {
		t.Fatal("blob 8's preview drew blob 7's leaf")
	}

	c.PutFetchedContent("w", after, rpc.ContentBasis{Version: 1}, c.AskContent("w"))
	if tr, ok := tree(8); !ok || names(tr) {
		t.Fatalf("blob 8's own bytes should decode without the leaf: ok=%v", ok)
	}
}

// This client's own layout write: its echo overtakes its response, the cache
// holds the last bytes until the response files the new ones
// (cache.BeginWrite), and a frame drawn in between decodes those last bytes.
// Once the new bytes are filed the preview must draw them, never the tree it
// decoded while the write was in flight.
func TestAPreviewRedrawsWhenItsOwnWriteFilesTheBytes(t *testing.T) {
	withGone := pane.TreeAtPlace("", "g0", nil, rpc.ViewOf(0, 0, 1))
	withGone.FocusedPane().Stack = pane.StackAt("g0", nil, "gone")
	before, _, err := pane.EncodeLayout(withGone, nil)
	if err != nil {
		t.Fatal(err)
	}
	after, _, err := pane.EncodeLayout(pane.TreeAtPlace("", "g0", nil, rpc.ViewOf(0, 0, 1)), nil)
	if err != nil {
		t.Fatal(err)
	}
	row := func(blob int64) *gridwellv1.Tile {
		return &gridwellv1.Tile{Id: "w", GridId: "g", Kind: rpc.KindWorkspace, Version: 1, BlobId: blob}
	}
	c := cache.New()
	c.PutGrid(&gridwellv1.Grid{Id: "g"}, []*gridwellv1.Tile{row(7)})
	c.PutFetchedContent("w", before, rpc.ContentBasis{Version: 1}, c.AskContent("w"))
	l := NewLayouts(nil)
	tree := func(blob int64) (*pane.Tree, bool) {
		return l.Tree("w", blob, c.BodyGen("w"), func() ([]byte, bool) { return c.TileContent("w") })
	}
	names := func(tr *pane.Tree) bool {
		found := false
		tr.Walk(func(p *pane.Pane) { found = found || p.ContentID() == "gone" })
		return found
	}

	end := c.BeginWrite("w")
	c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{TileChanged: &gridwellv1.TileChanged{Tile: row(8)}}})
	tree(8)
	c.PutSavedContent(row(8), after)
	c.PutWriteResponse("g", row(8), cache.WroteBody)
	end()
	if tr, ok := tree(8); !ok || names(tr) {
		t.Fatalf("after its own write landed the preview still draws the layout it replaced: ok=%v", ok)
	}
}
