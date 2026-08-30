package local_test

import (
	"context"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/local"
)

func createTile(t *testing.T, p *local.Plugin, gridID string, tile *gridwellv1.Tile, data []byte) *gridwellv1.Tile {
	t.Helper()
	r, err := p.CreateTile(context.Background(), &gridwellv1.CreateTileRequest{GridId: gridID, Tile: tile})
	if err != nil {
		t.Fatalf("CreateTile(%s): %v", tile.Kind, err)
	}
	out := r.Tile
	if len(data) > 0 {
		// Creation is metadata-only; the body follows through the one write.
		w, err := p.Store().WriteContent(context.Background(), out.Id, out.Version, data)
		if err != nil {
			t.Fatalf("WriteContent(%s): %v", tile.Kind, err)
		}
		out = getTile(t, p, out.Id)
		_ = w
	}
	return out
}

func getTile(t *testing.T, p *local.Plugin, id string) *gridwellv1.Tile {
	t.Helper()
	r, err := p.GetTile(context.Background(), &gridwellv1.GetTileRequest{TileId: id})
	if err != nil {
		t.Fatalf("GetTile: %v", err)
	}
	return r.Tile
}

// TestSetTileDispatchVersionSemantics proves the single SetTile writeback routes
// each kind to the right store op AND that the version rule rides across the
// dispatch seam: NOTHING SetTile can write is a user content edit — well and
// text framing, a url freeze, a shell's frozen frame are all framing or
// automatic captures — so no arm may bump (docs/simplify-plan.md S5;
// store/version_rule_test.go is the whole table). The user's own edits reach
// the store by other verbs: WriteContent and the rename arm.
func TestSetTileDispatchVersionSemantics(t *testing.T) {
	p := openPlugin(t)
	root := rootGrid(t, p)
	ctx := context.Background()

	// well framing rides its OWN verb now (SetFraming — one verb for the
	// doorway tile and the root grid alike), so SetTile refuses the kind
	// rather than leaving the mapping ambiguous. Still no version bump.
	well := createTile(t, p, root, &gridwellv1.Tile{Kind: "well", X: 0, Y: 0, W: 1, H: 1}, nil)
	if _, err := p.SetTile(ctx, &gridwellv1.SetTileRequest{TileId: well.Id, Version: well.Version,
		Tile: &gridwellv1.Tile{Kind: "well", ViewCx: 3, ViewCy: 4, ViewZoom: 2}}); err == nil {
		t.Error("SetTile still accepts well framing; it must route through SetFraming")
	}
	if _, err := p.SetFraming(ctx, &gridwellv1.SetFramingRequest{TileId: well.Id, Version: well.Version,
		Cx: 3, Cy: 4, Zoom: 2}); err != nil {
		t.Fatalf("SetFraming well: %v", err)
	}
	if v := getTile(t, p, well.Id).Version; v != well.Version {
		t.Errorf("well framing bumped version %d -> %d", well.Version, v)
	}

	// text framing: no bump.
	text := createTile(t, p, root, &gridwellv1.Tile{Kind: "text", X: 2, Y: 0, W: 2, H: 2}, []byte("# t"))
	if _, err := p.SetTile(ctx, &gridwellv1.SetTileRequest{TileId: text.Id, Version: text.Version,
		Tile: &gridwellv1.Tile{Kind: "text", TextW: 100, TextMode: "rendered"}}); err != nil {
		t.Fatalf("SetTile text: %v", err)
	}
	if v := getTile(t, p, text.Id).Version; v != text.Version {
		t.Errorf("text framing bumped version %d -> %d", text.Version, v)
	}

	// url freeze: an automatic capture, no bump.
	url := createTile(t, p, root, &gridwellv1.Tile{Kind: "url", X: 4, Y: 0, W: 2, H: 2, UrlString: "https://a"}, nil)
	if _, err := p.SetTile(ctx, &gridwellv1.SetTileRequest{TileId: url.Id, Version: url.Version,
		Tile: &gridwellv1.Tile{Kind: "url", UrlString: "https://b", AltText: "B"}, Preview: []byte("jpg")}); err != nil {
		t.Fatalf("SetTile url: %v", err)
	}
	if v := getTile(t, p, url.Id).Version; v != url.Version {
		t.Errorf("url freeze bumped version %d -> %d; a capture is not an edit", url.Version, v)
	}

	// shell preview: an automatic capture, no bump.
	shell := createTile(t, p, root, &gridwellv1.Tile{Kind: "shell", X: 6, Y: 0, W: 2, H: 2}, nil)
	if _, err := p.SetTile(ctx, &gridwellv1.SetTileRequest{TileId: shell.Id, Version: shell.Version,
		Tile: &gridwellv1.Tile{Kind: "shell"}, Preview: []byte("jpg")}); err != nil {
		t.Fatalf("SetTile shell: %v", err)
	}
	if v := getTile(t, p, shell.Id).Version; v != shell.Version {
		t.Errorf("shell preview bumped version %d -> %d; a capture is not an edit", shell.Version, v)
	}
}

// TestSetAndCreateRejectBadKinds: the dispatch surfaces a clear error for a nil
// tile and an unknown kind rather than silently no-op'ing.
func TestSetAndCreateRejectBadKinds(t *testing.T) {
	p := openPlugin(t)
	root := rootGrid(t, p)
	ctx := context.Background()

	if _, err := p.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: root, Tile: nil}); err == nil {
		t.Error("CreateTile(nil) should error")
	}
	if _, err := p.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: root, Tile: &gridwellv1.Tile{Kind: "bogus", W: 1, H: 1}}); err == nil {
		t.Error("CreateTile(unknown kind) should error")
	}
	if _, err := p.SetTile(ctx, &gridwellv1.SetTileRequest{Tile: nil}); err == nil {
		t.Error("SetTile(nil) should error")
	}
}

// TestCloneTileThroughPlugin: cloning routes through the plugin and yields an
// independent tile (new row id, new interior child grid) — the eager-copy rule
// holds across the RPC boundary, not just in the store.
func TestCloneTileThroughPlugin(t *testing.T) {
	p := openPlugin(t)
	root := rootGrid(t, p)
	ctx := context.Background()

	well := createTile(t, p, root, &gridwellv1.Tile{Kind: "well", X: 0, Y: 0, W: 1, H: 1}, nil)
	clone, err := p.CloneTile(ctx, &gridwellv1.CloneTileRequest{
		TileId: well.Id, Version: well.Version, DestGridId: root, X: 5, Y: 0,
	})
	if err != nil {
		t.Fatalf("CloneTile: %v", err)
	}
	if clone.Tile.Id == well.Id {
		t.Error("clone reused the source row id")
	}
	if clone.Tile.ChildGridId == well.ChildGridId {
		t.Error("clone shares the source's interior child grid (should be an independent copy)")
	}
}

// TestDeleteExitWellThroughPluginNoCascade: deleting an exit well via the plugin
// removes the tile but does not touch the remote grid it referenced — the
// plugin's delete honors the shared-reference semantics end to end.
func TestDeleteExitWellThroughPluginNoCascade(t *testing.T) {
	p := openPlugin(t)
	root := rootGrid(t, p)
	ctx := context.Background()

	ew := createTile(t, p, root, &gridwellv1.Tile{Kind: "well", X: 0, Y: 0, W: 1, H: 1, ChildGridId: "remote-uuid/7", AltText: "remote"}, nil)
	if ew.ChildGridId != "remote-uuid/7" {
		t.Fatalf("exit well child = %q, want remote-uuid/7", ew.ChildGridId)
	}
	if _, err := p.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: ew.Id, Version: ew.Version}); err != nil {
		t.Fatalf("DeleteTile exit well: %v", err)
	}
	// It's gone from the grid, and the delete didn't error trying to GC a
	// non-local child.
	g, err := p.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: root})
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range g.Tiles {
		if tl.Id == ew.Id {
			t.Error("exit well still present after delete")
		}
	}
}

// TestPaneDispatch: the pane kind's plugin-level contract. CreateTile makes
// the metadata row; the layout blob follows through WriteContent (framing —
// no version bump); SetTile REFUSES the kind (the layout rides the content
// door — the mapping stays total, nothing falls to a silent no-op).
func TestPaneDispatch(t *testing.T) {
	p := openPlugin(t)
	root := rootGrid(t, p)
	ctx := context.Background()

	pt := createTile(t, p, root, &gridwellv1.Tile{Kind: "pane", X: 0, Y: 4, W: 2, H: 2, AltText: "ws"},
		[]byte(`{"v":1,"root":{"pane":{"id":"p1","zoom":1}},"focus":"p1"}`))
	if pt.Kind != "pane" || pt.BlobId == 0 || pt.AltText != "ws" {
		t.Fatalf("pane create: %+v", pt)
	}

	if _, err := p.SetTile(ctx, &gridwellv1.SetTileRequest{TileId: pt.Id, Version: pt.Version,
		Tile: &gridwellv1.Tile{Kind: "pane"}}); err == nil {
		t.Error("SetTile on a pane tile must be refused (layout rides SetPaneLayout)")
	}

	if _, err := p.Store().WriteContent(ctx, pt.Id, pt.Version,
		[]byte(`{"v":1,"root":{"split":{"dir":"v","ratio":0.5,"a":{"pane":{"id":"p1","zoom":1}},"b":{"pane":{"id":"p2","zoom":1}}}},"focus":"p2"}`),
	); err != nil {
		t.Fatalf("WriteContent(layout): %v", err)
	}
	after := getTile(t, p, pt.Id)
	if after.Version != pt.Version {
		t.Errorf("layout write bumped version %d -> %d (framing must not)", pt.Version, after.Version)
	}
	if after.BlobId == pt.BlobId {
		t.Errorf("layout write did not move the blob")
	}

	// The layout reads back over the generic content path with the codec's
	// media type (self-describing).
	data, mediaType, _, err := p.Store().ReadContent(ctx, pt.Id)
	if err != nil {
		t.Fatalf("ReadContent: %v", err)
	}
	if mediaType != "application/vnd.gridwell.pane-layout+json" {
		t.Errorf("media_type = %q", mediaType)
	}
	if len(data) == 0 {
		t.Error("layout content empty")
	}
}
