package server

import (
	"context"
	"math"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// A content zoom crosses the client, the web door and the router to one of
// two writers: the store's own for a home tile, the plugin adapter's for a
// plugin tile. Both must refuse what the column cannot mean, and write a
// valid value exactly.
func TestContentZoomRefusedAtEitherWriter(t *testing.T) {
	cl, st, _, fsRoot := lazyStack(t)
	ctx := context.Background()

	lp, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	home, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: rpc.HomeGrid(lp),
		Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 0, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	pg, err := cl.GetGrid(ctx, fsGrid(t, cl, fsRoot, 1))
	if err != nil {
		t.Fatal(err)
	}
	var file *gridwellv1.Tile
	for _, tile := range pg.Tiles {
		if tile.AltText == "f0.txt" {
			file = tile
		}
	}
	if file == nil {
		t.Fatal("no f0.txt in the plugin grid")
	}

	for _, c := range []struct{ name, id string }{{"home tile", home.Id}, {"plugin tile", file.Id}} {
		t.Run(c.name, func(t *testing.T) {
			before, err := cl.GetTile(ctx, c.id)
			if err != nil {
				t.Fatal(err)
			}
			tiles0, grids0 := pluginRows(t, st)
			for _, z := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), 0, -1,
				rpc.ContentZoomMin - 0.01, rpc.ContentZoomMax + 0.01, 1e9} {
				_, err := cl.SetContentZoom(ctx, c.id, z)
				if connect.CodeOf(err) != connect.CodeInvalidArgument {
					t.Errorf("SetContentZoom(%v) = %v, want InvalidArgument", z, err)
				}
			}
			after, err := cl.GetTile(ctx, c.id)
			if err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(before, after) {
				t.Errorf("a refused zoom changed the tile:\nbefore %v\nafter  %v", before, after)
			}
			if tiles, grids := pluginRows(t, st); tiles != tiles0 || grids != grids0 {
				t.Errorf("a refused zoom minted rows: %d/%d -> %d/%d", tiles0, grids0, tiles, grids)
			}

			for _, z := range []float64{rpc.ContentZoomMin, 1.25, rpc.ContentZoomMax} {
				got, err := cl.SetContentZoom(ctx, c.id, z)
				if err != nil {
					t.Fatalf("SetContentZoom(%v): %v", z, err)
				}
				if got.ContentZoom != z {
					t.Errorf("SetContentZoom(%v) answered %v", z, got.ContentZoom)
				}
				read, err := cl.GetTile(ctx, c.id)
				if err != nil {
					t.Fatal(err)
				}
				if math.Float64bits(read.ContentZoom) != math.Float64bits(z) {
					t.Errorf("stored %v, want %v exactly", read.ContentZoom, z)
				}
			}
		})
	}
}
