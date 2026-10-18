package sourcecache

import (
	"context"
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// A tile the source stopped listing must not keep its cached body: once
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
	writeOne(t, cc, id, doomed.GetTile().GetVersion(), []byte("cached words"))
	readContent(t, cc, id)
	if _, err := cc.GetGrid(ctx, &pb.GetGridRequest{GridId: root}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := cc.db.QueryRow(`SELECT COUNT(1) FROM content WHERE tile_id = ?`, id).Scan(&n); err != nil || n != 1 {
		t.Fatalf("setup: cached bodies = %d (%v), want 1", n, err)
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
		t.Errorf("the gone tile's cached body survived: %d content row(s) no tile row names", n)
	}
}

// Every way a tile row leaves frees what was kept under its id, and only when
// no row names the id any more: a tile that moved to another grid keeps its own.
func TestATileRowLeavingFreesOnlyWhatNoRowNames(t *testing.T) {
	cc, _, _, _ := fixture(t)
	ctx := context.Background()
	keep := func(id string) {
		t.Helper()
		for _, q := range []string{
			`INSERT INTO content (tile_id, media_type, version, data, fetched_at) VALUES (?, 'text/plain', 1, x'61', 0)`,
			`INSERT INTO contentstamps (tile_id, stamp) VALUES (?, 's')`,
			`INSERT INTO previews (tile_id, jpeg, fetched_at) VALUES (?, x'00', 0)`,
			`INSERT INTO servecontent (tile_id, subpath, status, media_type, data, fetched_at) VALUES (?, '', 200, 'text/html', x'00', 0)`,
		} {
			if _, err := cc.db.Exec(q, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	kept := func(id string) int {
		t.Helper()
		total := 0
		for _, table := range tileKeyed {
			var n int
			if err := cc.db.QueryRow(`SELECT COUNT(1) FROM `+table+` WHERE tile_id = ?`, id).Scan(&n); err != nil {
				t.Fatal(err)
			}
			total += n
		}
		return total
	}
	grid := func(id string, tiles ...string) *pb.GetGridResponse {
		r := &pb.GetGridResponse{Grid: &pb.Grid{Id: id}}
		for _, tid := range tiles {
			r.Tiles = append(r.Tiles, &pb.Tile{Id: tid, GridId: id, Kind: "text"})
		}
		return r
	}
	cc.storeGrid(ctx, "ga", grid("ga", "mover", "evicted"))
	cc.storeGrid(ctx, "gb", grid("gb", "removed"))
	for _, id := range []string{"mover", "evicted", "removed"} {
		keep(id)
	}

	cc.storeGrid(ctx, "gb", grid("gb", "removed", "mover"))
	cc.storeGrid(ctx, "ga", grid("ga", "evicted"))
	if n := kept("mover"); n != len(tileKeyed) {
		t.Errorf("a tile that moved grids lost what was kept under it: %d of %d rows", n, len(tileKeyed))
	}
	cc.evictGrid(ctx, "ga")
	if n := kept("evicted"); n != 0 {
		t.Errorf("an evicted grid's tile kept %d row(s)", n)
	}
	cc.deleteTile(ctx, "removed")
	if n := kept("removed"); n != 0 {
		t.Errorf("a removed tile kept %d row(s)", n)
	}
}
