package server_test

import (
	"context"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// A far node's home is reached through a door in the near home, descended
// into and edited, and its tiles live on the far node, never in the near
// home that holds only the door.
func TestAFarHomeThroughADoor(t *testing.T) {
	cl, near, far := newTwoHomes(t, nil)
	ctx := context.Background()

	door, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: near, Tile: &gridwellv1.Tile{Kind: rpc.KindDoor, X: 0, Y: 0, W: 1, H: 1, ChildGridId: far, AltText: "far"}})
	if err != nil {
		t.Fatalf("door onto the far home: %v", err)
	}
	if door.ChildGridId != far || !door.Link || !in(door.Id, localNodeID) {
		t.Fatalf("door = %v, want a near link door onto %s", door, far)
	}
	if g, err := cl.GetGrid(ctx, door.ChildGridId); err != nil || len(g.Tiles) != 0 {
		t.Fatalf("the far home through the door = %v, %v; want it empty", g, err)
	}
	txt, err := cl.CreateWithContent(ctx, &gridwellv1.CreateTileRequest{GridId: door.ChildGridId, Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 0, Y: 0, W: 1, H: 1}}, []byte("# far"))
	if err != nil {
		t.Fatalf("create in the far home: %v", err)
	}
	if !in(txt.Id, farHome) {
		t.Errorf("the text lives at %q, want the far home %q", txt.Id, farHome)
	}
	if body, _, _, err := cl.ReadContent(ctx, txt.Id); err != nil || string(body) != "# far" {
		t.Errorf("content = %q, %v", body, err)
	}
	if g, err := cl.GetGrid(ctx, far); err != nil || len(g.Tiles) != 1 || g.Tiles[0].Id != txt.Id {
		t.Errorf("the far home = %v, %v; want just the text", g, err)
	}
	g, err := cl.GetGrid(ctx, near)
	if err != nil {
		t.Fatal(err)
	}
	for _, tile := range g.Tiles {
		if tile.Id == txt.Id {
			t.Error("the far home's tile leaked into the near home")
		}
	}
}
