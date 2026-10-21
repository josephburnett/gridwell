package local_test

import (
	"context"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// A url created with a name keeps it through a page-title capture only when
// the create says the name was typed; one created with a captured name, or
// none, takes the title the capture brings.
func TestAURLCreatedNamedKeepsItsNameThroughACapture(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name  string
		named bool
		want  string
	}{
		{"", false, "Page title"},
		{"their title", false, "Page title"},
		{"mine", true, "mine"},
	} {
		p := openPlugin(t)
		created, err := p.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: rootGrid(t, p),
			Tile: &gridwellv1.Tile{Kind: rpc.KindURL, W: 1, H: 1,
				UrlString: "https://example.com", AltText: c.name, Named: c.named}})
		if err != nil {
			t.Fatalf("CreateTile(url %q): %v", c.name, err)
		}
		if created.Tile.AltText != c.name || created.Tile.Named != c.named {
			t.Fatalf("created as %q named=%v, want %q named=%v",
				created.Tile.AltText, created.Tile.Named, c.name, c.named)
		}
		if _, err := p.SetTile(ctx, &gridwellv1.SetTileRequest{TileId: created.Tile.Id,
			Tile: &gridwellv1.Tile{Kind: rpc.KindURL, AltText: "Page title"}}); err != nil {
			t.Fatalf("capture: %v", err)
		}
		got, err := p.GetTile(ctx, &gridwellv1.GetTileRequest{TileId: created.Tile.Id})
		if err != nil {
			t.Fatal(err)
		}
		if got.Tile.AltText != c.want {
			t.Errorf("created %q named=%v, captured: alt = %q, want %q", c.name, c.named, got.Tile.AltText, c.want)
		}
	}
}

// Every kind created with a name carries whether it was typed onto the row.
func TestEveryNamedCreateLatches(t *testing.T) {
	ctx := context.Background()
	p := openPlugin(t)
	root := rootGrid(t, p)
	const far = "aabbccddaabbccddaabbccddaabbccdd/7"
	for i, tl := range []*gridwellv1.Tile{
		{Kind: rpc.KindURL, UrlString: "https://example.com"},
		{Kind: rpc.KindDoor},
		{Kind: rpc.KindDoor, ChildGridId: far},
		{Kind: rpc.KindWorkspace},
		{Kind: rpc.KindText, LinkTargetId: far},
	} {
		for j, named := range []bool{false, true} {
			tl.X, tl.Y, tl.W, tl.H, tl.AltText, tl.Named = int64(i), int64(j), 1, 1, "name", named
			created, err := p.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: root, Tile: tl})
			if err != nil {
				t.Fatalf("create %s: %v", tl.Kind, err)
			}
			if created.Tile.Named != named {
				t.Errorf("a %s (link %v) created named=%v reads named=%v",
					tl.Kind, created.Tile.LinkTargetId != "" || tl.ChildGridId != "", named, created.Tile.Named)
			}
		}
	}
}

// A SetTile cannot latch a name: only the rename does.
func TestSetTileCannotLatchAName(t *testing.T) {
	ctx := context.Background()
	p := openPlugin(t)
	created, err := p.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: rootGrid(t, p),
		Tile: &gridwellv1.Tile{Kind: rpc.KindURL, W: 1, H: 1, UrlString: "https://example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.SetTile(ctx, &gridwellv1.SetTileRequest{TileId: created.Tile.Id,
		Tile: &gridwellv1.Tile{Kind: rpc.KindURL, AltText: "Page title", Named: true}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Tile.Named {
		t.Errorf("a SetTile latched the name: %+v", got.Tile)
	}
}
