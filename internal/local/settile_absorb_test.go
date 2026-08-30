package local_test

import (
	"context"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// The scalar SetTile operations: rename and content_zoom ride the one
// writeback, one operation per call.

func TestSetTileRenameArm(t *testing.T) {
	p := openPlugin(t)
	root := rootGrid(t, p)
	ctx := context.Background()

	url := createTile(t, p, root, &gridwellv1.Tile{Kind: "url", X: 0, Y: 0, W: 2, H: 2, UrlString: "https://a"}, nil)

	// The rename is versioned now: a stale claim is refused.
	if _, err := p.SetTile(ctx, &gridwellv1.SetTileRequest{
		TileId: url.Id, Version: url.Version + 5, Rename: "My Name",
	}); err == nil {
		t.Fatal("stale rename claim must be refused")
	}

	r, err := p.SetTile(ctx, &gridwellv1.SetTileRequest{
		TileId: url.Id, Version: url.Version, Rename: "My Name",
	})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if r.Tile.AltText != "My Name" {
		t.Errorf("alt = %q, want My Name", r.Tile.AltText)
	}
	if r.Tile.Version <= url.Version {
		t.Errorf("a rename is a user edit: version must bump (%d -> %d)", url.Version, r.Tile.Version)
	}

	// A later automatic title capture, the url arm's tile.alt_text, defers
	// to it.
	if _, err := p.SetTile(ctx, &gridwellv1.SetTileRequest{
		TileId: url.Id, Version: r.Tile.Version,
		Tile: &gridwellv1.Tile{Kind: "url", AltText: "Captured Title"},
	}); err != nil {
		t.Fatalf("freeze: %v", err)
	}
	if got := getTile(t, p, url.Id).AltText; got != "My Name" {
		t.Errorf("auto capture clobbered the user rename: %q", got)
	}

	// Text tiles derive their name from the first line; rename refused.
	text := createTile(t, p, root, &gridwellv1.Tile{Kind: "text", X: 3, Y: 0, W: 1, H: 1}, []byte("# n"))
	if _, err := p.SetTile(ctx, &gridwellv1.SetTileRequest{
		TileId: text.Id, Version: text.Version, Rename: "nope",
	}); err == nil {
		t.Error("text rename must be refused")
	}
}

func TestSetTileContentZoomArm(t *testing.T) {
	p := openPlugin(t)
	root := rootGrid(t, p)
	ctx := context.Background()
	zoom := func(v float64) *float64 { return &v }

	text := createTile(t, p, root, &gridwellv1.Tile{Kind: "text", X: 0, Y: 0, W: 2, H: 2}, []byte("# t"))
	r, err := p.SetTile(ctx, &gridwellv1.SetTileRequest{
		TileId: text.Id, Version: text.Version, ContentZoom: zoom(1.5),
	})
	if err != nil {
		t.Fatalf("content zoom: %v", err)
	}
	if r.Tile.ContentZoom != 1.5 {
		t.Errorf("content_zoom = %v, want 1.5", r.Tile.ContentZoom)
	}
	if r.Tile.Version != text.Version {
		t.Errorf("content zoom is framing: version must not bump (%d -> %d)", text.Version, r.Tile.Version)
	}

	// Wells are refused: their view_zoom is the grid viewport, not
	// content.
	well := createTile(t, p, root, &gridwellv1.Tile{Kind: "well", X: 3, Y: 0, W: 1, H: 1}, nil)
	if _, err := p.SetTile(ctx, &gridwellv1.SetTileRequest{
		TileId: well.Id, Version: well.Version, ContentZoom: zoom(2),
	}); err == nil {
		t.Error("well content zoom must be refused")
	}
}

// TestSetTileURLFrozenArm pins the standing-freeze bit: it is framing, so no
// version bump; set and clear both round-trip; and it is refused off the url
// kind.
func TestSetTileURLFrozenArm(t *testing.T) {
	p := openPlugin(t)
	root := rootGrid(t, p)
	ctx := context.Background()
	frozen := func(v bool) *bool { return &v }

	url := createTile(t, p, root, &gridwellv1.Tile{Kind: "url", X: 0, Y: 0, W: 2, H: 2, UrlString: "https://a"}, nil)
	r, err := p.SetTile(ctx, &gridwellv1.SetTileRequest{
		TileId: url.Id, Version: url.Version, UrlFrozen: frozen(true),
	})
	if err != nil {
		t.Fatalf("freeze: %v", err)
	}
	if !r.Tile.UrlFrozen {
		t.Error("url_frozen did not set")
	}
	if r.Tile.Version != url.Version {
		t.Errorf("url_frozen is framing: version must not bump (%d -> %d)", url.Version, r.Tile.Version)
	}

	// Clearing, the reconnect gesture, is a distinct false write, not an
	// absent field.
	r, err = p.SetTile(ctx, &gridwellv1.SetTileRequest{
		TileId: url.Id, Version: url.Version, UrlFrozen: frozen(false),
	})
	if err != nil {
		t.Fatalf("unfreeze: %v", err)
	}
	if r.Tile.UrlFrozen {
		t.Error("url_frozen did not clear")
	}

	// Only url tiles carry the intent.
	text := createTile(t, p, root, &gridwellv1.Tile{Kind: "text", X: 3, Y: 0, W: 1, H: 1}, []byte("# t"))
	if _, err := p.SetTile(ctx, &gridwellv1.SetTileRequest{
		TileId: text.Id, Version: text.Version, UrlFrozen: frozen(true),
	}); err == nil {
		t.Error("url_frozen on a text tile must be refused")
	}
}

func TestSetTileOneOperationPerCall(t *testing.T) {
	p := openPlugin(t)
	root := rootGrid(t, p)
	ctx := context.Background()
	zoom := func(v float64) *float64 { return &v }
	frozen := func(v bool) *bool { return &v }

	url := createTile(t, p, root, &gridwellv1.Tile{Kind: "url", X: 0, Y: 0, W: 2, H: 2, UrlString: "https://a"}, nil)

	cases := []*gridwellv1.SetTileRequest{
		{TileId: url.Id, Version: url.Version, Rename: "n", ContentZoom: zoom(1.5)},
		{TileId: url.Id, Version: url.Version, Rename: "n", Tile: &gridwellv1.Tile{Kind: "url"}},
		{TileId: url.Id, Version: url.Version, ContentZoom: zoom(1.5), Tile: &gridwellv1.Tile{Kind: "url"}},
		{TileId: url.Id, Version: url.Version, UrlFrozen: frozen(true), Rename: "n"},
		{TileId: url.Id, Version: url.Version, UrlFrozen: frozen(true), Tile: &gridwellv1.Tile{Kind: "url"}},
	}
	for i, req := range cases {
		if _, err := p.SetTile(ctx, req); err == nil {
			t.Errorf("case %d: combined operations must be refused", i)
		}
	}
}

func TestPlaceTileDispatch(t *testing.T) {
	p := openPlugin(t)
	root := rootGrid(t, p)
	ctx := context.Background()

	text := createTile(t, p, root, &gridwellv1.Tile{Kind: "text", X: 0, Y: 0, W: 1, H: 1}, []byte("x"))
	r, err := p.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{
		TileId: text.Id, GridId: root, X: 4, Y: 5, W: 2, H: 3,
	})
	if err != nil {
		t.Fatalf("place: %v", err)
	}
	if r.Tile.X != 4 || r.Tile.Y != 5 || r.Tile.W != 2 || r.Tile.H != 3 {
		t.Errorf("placed (%d,%d %dx%d), want (4,5 2x3)", r.Tile.X, r.Tile.Y, r.Tile.W, r.Tile.H)
	}
}
