package server_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// A write of something the user placed that names no claim is refused at
// either API with the claim it lacks, and the row stands: the lock cannot be
// bypassed by omission. A tile on a far node behaves the same as one at home.
// A select is the decided exception: it claims nothing and lands at either
// API.
func TestTheAPIsRefuseAUserWriteThatNamesNoClaim(t *testing.T) {
	ctx := context.Background()
	m := staleMesh(t)
	me := m.nodes[meNode]
	yes := true
	type api struct {
		place  func(*gridwellv1.MoveTileRequest) error
		delete func(*gridwellv1.DeleteTileRequest) error
		set    func(*gridwellv1.SetTileRequest) error
		sel    func(*gridwellv1.SetSelectionRequest) error
		code   func(error) codes.Code
	}
	apis := map[string]api{
		"web API": {
			place: func(r *gridwellv1.MoveTileRequest) error {
				_, err := me.web.MoveTile(ctx, connect.NewRequest(r))
				return err
			},
			delete: func(r *gridwellv1.DeleteTileRequest) error {
				_, err := me.web.DeleteTile(ctx, connect.NewRequest(r))
				return err
			},
			set: func(r *gridwellv1.SetTileRequest) error {
				_, err := me.web.SetTile(ctx, connect.NewRequest(r))
				return err
			},
			sel: func(r *gridwellv1.SetSelectionRequest) error {
				_, err := me.web.SetSelection(ctx, connect.NewRequest(r))
				return err
			},
			code: func(err error) codes.Code { return codes.Code(connect.CodeOf(err)) },
		},
		"connection API": {
			place:  func(r *gridwellv1.MoveTileRequest) error { _, err := me.far.MoveTile(ctx, r); return err },
			delete: func(r *gridwellv1.DeleteTileRequest) error { _, err := me.far.DeleteTile(ctx, r); return err },
			set:    func(r *gridwellv1.SetTileRequest) error { _, err := me.far.SetTile(ctx, r); return err },
			sel:    func(r *gridwellv1.SetSelectionRequest) error { _, err := me.far.SetSelection(ctx, r); return err },
			code:   status.Code,
		},
	}
	x := int64(50)
	for name, d := range apis {
		for where, grid := range staleWhere(m) {
			t.Run(name+"/"+where, func(t *testing.T) {
				refused := func(verb string, err error, want string) {
					t.Helper()
					if err == nil || d.code(err) != codes.InvalidArgument || !strings.Contains(err.Error(), want) {
						t.Fatalf("claimless %s: got %v, want InvalidArgument naming %q", verb, err, want)
					}
				}
				x += 2
				tile := m.text(ctx, meNode, grid, x, "# stays")
				refused("move", d.place(&gridwellv1.MoveTileRequest{TileId: tile.Id, GridId: grid,
					X: x + 1, Y: 0, W: 1, H: 1}), "claim")
				refused("delete", d.delete(&gridwellv1.DeleteTileRequest{TileId: tile.Id, ClaimVersion: tile.Version}), "claim")
				if got := m.tileIn(ctx, meNode, grid, tile.Id); got.X != x {
					t.Fatalf("x = %d, want %d standing", got.X, x)
				}

				page, err := me.cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: grid,
					Tile: &gridwellv1.Tile{Kind: rpc.KindURL, X: x, Y: 2, W: 1, H: 1, UrlString: "https://example.com/"}})
				if err != nil {
					t.Fatal(err)
				}
				refused("freeze", d.set(&gridwellv1.SetTileRequest{TileId: page.Id, UrlFrozen: &yes}), "claim_frozen")
				if got := m.tileIn(ctx, meNode, grid, page.Id); got.UrlFrozen {
					t.Fatal("a claimless freeze landed")
				}

				if err := d.sel(&gridwellv1.SetSelectionRequest{GridId: grid, Add: []string{tile.Id}}); err != nil {
					t.Fatalf("a select: %v, want it to land", err)
				}
				g, err := me.cl.GetGrid(ctx, grid)
				if err != nil {
					t.Fatal(err)
				}
				if !slices.Contains(g.Grid.Selection, tile.Id) {
					t.Fatalf("selection = %v, want %s in it", g.Grid.Selection, tile.Id)
				}
			})
		}
	}
}
