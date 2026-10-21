package local_test

import (
	"context"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// A url created with a name keeps it through a page-title capture, as a typed
// name does; one created with none takes the title the capture brings.
func TestAURLCreatedWithANameKeepsItThroughACapture(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name, want string
	}{
		{"", "Page title"},
		{"mine", "mine"},
	} {
		p := openPlugin(t)
		created, err := p.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: rootGrid(t, p),
			Tile: &gridwellv1.Tile{Kind: rpc.KindURL, W: 1, H: 1,
				UrlString: "https://example.com", AltText: c.name}})
		if err != nil {
			t.Fatalf("CreateTile(url %q): %v", c.name, err)
		}
		if created.Tile.AltText != c.name {
			t.Fatalf("created as %q, want %q", created.Tile.AltText, c.name)
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
			t.Errorf("created named %q, captured: alt = %q, want %q", c.name, got.Tile.AltText, c.want)
		}
	}
}
