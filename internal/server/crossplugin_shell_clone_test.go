package server

import (
	"context"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// A tmux session lives on the node that started it, so a shell cloned into
// another namespace cannot share it there: what the holding node can reach,
// you can link, and the copy is a link to its source, at the top of the
// gesture and inside a deep-copied well alike.
func TestCloneShellAcrossPluginsLinksItsSource(t *testing.T) {
	cl, _, rootA, _, rootB := twoPluginServer(t)
	ctx := context.Background()

	sh, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: rootA,
		Tile: &gridwellv1.Tile{Kind: rpc.KindShell, X: 0, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	top, err := cl.CloneTile(ctx, &gridwellv1.CloneTileRequest{TileId: sh.Id, DestGridId: rootB, X: 3, Y: 3})
	if err != nil {
		t.Fatalf("cross-plugin shell clone: %v", err)
	}
	if top.Kind != rpc.KindShell || top.LinkTargetId != sh.Id || !top.Reference {
		t.Errorf("top-level copy = kind %q target %q reference %v, want a shell link to %q",
			top.Kind, top.LinkTargetId, top.Reference, sh.Id)
	}

	well, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: rootA,
		Tile: &gridwellv1.Tile{Kind: rpc.KindWell, X: 2, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	inner, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: well.ChildGridId,
		Tile: &gridwellv1.Tile{Kind: rpc.KindShell, X: 0, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	wc, err := cl.CloneTile(ctx, &gridwellv1.CloneTileRequest{TileId: well.Id, DestGridId: rootB, X: 6, Y: 6})
	if err != nil {
		t.Fatalf("cross-plugin well clone: %v", err)
	}
	g, err := cl.GetGrid(ctx, wc.ChildGridId)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(g.Tiles); n != 1 {
		t.Fatalf("copied well holds %d tiles, want 1", n)
	}
	if got := g.Tiles[0]; got.LinkTargetId != inner.Id || !got.Reference {
		t.Errorf("nested copy = target %q reference %v, want a shell link to %q", got.LinkTargetId, got.Reference, inner.Id)
	}
}
