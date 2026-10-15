package sourcecache

import (
	"context"
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// A tile the source stopped listing must not keep its remembered body: once
// the revalidation replaces the grid's tile set, no row names the tile, so a
// body left in content is bytes nothing can read and nothing will free.
func TestATileGoneWhileBlindTakesItsBodyWithIt(t *testing.T) {
	cc, upstream, root, _ := fixture(t)
	ctx := context.Background()

	doomed, err := cc.CreateTile(ctx, &pb.CreateTileRequest{GridId: root,
		Tile: &pb.Tile{Kind: "text", X: 0, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	id := doomed.GetTile().GetId()
	writeOne(t, cc, id, doomed.GetTile().GetVersion(), []byte("remembered words"))
	readContent(t, cc, id)
	if _, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := cc.db.QueryRow(`SELECT COUNT(1) FROM content WHERE tile_id = ?`, id).Scan(&n); err != nil || n != 1 {
		t.Fatalf("setup: remembered bodies = %d (%v), want 1", n, err)
	}
	// Deleted UPSTREAM directly, as on a far machine while this one is blind.
	if _, err := upstream.Namespace.DeleteTile(ctx, &pb.DeleteTileRequest{TileId: id}); err != nil {
		t.Fatal(err)
	}
	ageGrid(t, cc, root)
	if _, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root}); err != nil {
		t.Fatal(err)
	}
	awaitFresh(t, cc, root)

	var tiles int
	if err := cc.db.QueryRow(`SELECT COUNT(1) FROM tiles WHERE id = ?`, id).Scan(&tiles); err != nil {
		t.Fatal(err)
	}
	if tiles != 0 {
		t.Fatalf("setup: the gone tile's row survived the revalidation")
	}
	if err := cc.db.QueryRow(`SELECT COUNT(1) FROM content WHERE tile_id = ?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("the gone tile's remembered body survived: %d content row(s) no tile row names", n)
	}
}
