package server

import (
	"context"
	"math"
	"strings"
	"testing"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// A framing that is not a view is refused across the whole seam — client,
// web door, router, store — as InvalidArgument with the reason, on both rows
// framing can live on, and what was stored before stays. The 2026-10-04 trace
// stored NaN on the root as NULL and failed the tile arm with an internal
// NOT NULL error the client showed verbatim.
func TestSetFramingRefusesWhatIsNotAViewAcrossTheSeam(t *testing.T) {
	_, cl, root := newTestServer(t)
	ctx := context.Background()
	well, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: root,
		Tile: &gridwellv1.Tile{Kind: rpc.KindWell, X: 1, Y: 1, W: 9, H: 7}})
	if err != nil {
		t.Fatal(err)
	}
	good := mkFraming(0.43, 3.7, 0.65)
	for _, req := range []*gridwellv1.SetFramingRequest{
		{TileId: well.Id, Cx: good.Cx(), Cy: good.Cy(), Zoom: good.Zoom()},
		{RootGridId: root, Cx: good.Cx(), Cy: good.Cy(), Zoom: good.Zoom()},
	} {
		if _, err := cl.SetFraming(ctx, req); err != nil {
			t.Fatal(err)
		}
	}

	nan, inf := math.NaN(), math.Inf(1)
	for name, req := range map[string]*gridwellv1.SetFramingRequest{
		"tile NaN center": {TileId: well.Id, Cx: nan, Cy: nan, Zoom: 0.05},
		"tile zero zoom":  {TileId: well.Id, Cx: 4.5, Cy: 3.5},
		"tile Inf zoom":   {TileId: well.Id, Cx: 1, Cy: 1, Zoom: inf},
		"root NaN center": {RootGridId: root, Cx: nan, Cy: 1, Zoom: 0.0045},
		"root zero zoom":  {RootGridId: root, Cx: 1, Cy: 1},
		"root -Inf zoom":  {RootGridId: root, Cx: 1, Cy: 1, Zoom: -inf},
	} {
		_, err := cl.SetFraming(ctx, req)
		if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "framing") {
			t.Errorf("%s: SetFraming = %v, want InvalidArgument naming the framing", name, err)
		}
	}

	tile, err := cl.GetTile(ctx, well.Id)
	if err != nil {
		t.Fatal(err)
	}
	if got := rpc.ViewOf(tile.ViewCx, tile.ViewCy, tile.ViewZoom); got != rpc.Saved(good) {
		t.Errorf("the well's framing after the refusals = %+v, want %+v", got, good)
	}
	hs, err := cl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, pl := range hs.Plugins {
		if pl.RootGridId == root {
			if got := rpc.ViewOf(pl.RootViewCx, pl.RootViewCy, pl.RootViewZoom); got != rpc.Saved(good) {
				t.Errorf("the root's framing after the refusals = %+v, want %+v", got, good)
			}
		}
	}
}
