package server_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gen/gridwell/v1/gridwellv1connect"
	"github.com/josephburnett/gridwell/internal/config"
)

// A grid_id routes a search to the namespace that owns it, at any depth, and
// the filters ride every hop to the node holding the rows: here a far home's
// selection, asked for through a connection with an empty query.
func TestSearchFiltersRideToTheGridsOwner(t *testing.T) {
	ctx := context.Background()
	h := newTransportHarness(t, []config.ConnectionConfig{{Name: "geneva", Addr: "/s"}}, nil)
	raw := gridwellv1connect.NewGridwellClient(h.localHTTP.Client(), h.localURL, connect.WithProtoJSON())
	search := func(req *gridwellv1.SearchRequest) (*gridwellv1.SearchResponse, error) {
		t.Helper()
		resp, err := raw.Search(ctx, connect.NewRequest(req))
		if err != nil {
			return nil, err
		}
		return resp.Msg, nil
	}

	farRoot := localNodeID + "/geneva/rnode1/" + h.rootBare
	well, err := h.localCl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: farRoot,
		Tile: &gridwellv1.Tile{Kind: "well", X: 0, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	var inside []*gridwellv1.Tile
	for x := int64(0); x < 2; x++ {
		tile, err := h.localCl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: well.ChildGridId,
			Tile: &gridwellv1.Tile{Kind: "text", X: x, Y: 0, W: 1, H: 1}})
		if err != nil {
			t.Fatal(err)
		}
		inside = append(inside, tile)
	}
	if err := h.localCl.SetSelection(ctx, &gridwellv1.SetSelectionRequest{GridId: well.ChildGridId,
		Claim: &gridwellv1.SelectionClaim{}, TileIds: []string{inside[1].Id}}); err != nil {
		t.Fatal(err)
	}

	resp, err := search(&gridwellv1.SearchRequest{GridId: well.ChildGridId})
	if err != nil {
		t.Fatalf("grid search: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("grid search = %v, want the well's two tiles", resp.Results)
	}
	resp, err = search(&gridwellv1.SearchRequest{GridId: farRoot, Recurse: true, Selected: true})
	if err != nil {
		t.Fatalf("selected search: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Tile.Id != inside[1].Id {
		t.Fatalf("selected under the far root = %v, want %s, qualified as it was made", resp.Results, inside[1].Id)
	}
	if p := resp.Results[0].Path; len(p) != 1 || p[0].Id != well.Id {
		t.Errorf("path = %v, want the well", p)
	}

	if _, err := search(&gridwellv1.SearchRequest{Query: "x", Recurse: true}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("recurse with no grid: err = %v, want InvalidArgument with the reason", err)
	}
}
