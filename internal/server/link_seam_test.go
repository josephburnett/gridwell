package server_test

import (
	"context"
	"testing"
	"time"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/claimtest"
	"github.com/josephburnett/gridwell/internal/config"
)

// Every id a write carries into a far grid crosses the router and the
// connection, and each hop peels its own segment (rpc.Hop). A link that
// kept a segment would be stored on the far node as a foreign id and read back
// with the chain doubled, naming a namespace nobody declares.
func TestIdsWrittenIntoAFarGridComeBackAsSent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h := newTransportHarness(t, []config.ConnectionConfig{{Name: "geneva", Addr: "/s"}}, nil)
	lp, err := h.localCl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	localHome, farRoot := lp.HomeGridId, connectionRows(lp)[0].RootGridId

	room, err := h.localCl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: farRoot, Tile: &gridwellv1.Tile{Kind: rpc.KindDoor, X: 0, Y: 0, W: 2, H: 2}})
	if err != nil {
		t.Fatalf("create far door: %v", err)
	}
	farGrid := room.ChildGridId
	body := []byte("# far target")
	target, err := h.localCl.CreateWithContent(ctx, &gridwellv1.CreateTileRequest{GridId: farRoot, Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 4, Y: 0, W: 1, H: 1}}, body)
	if err != nil {
		t.Fatalf("create far text: %v", err)
	}

	// The same answer from the write and from a read of the grid.
	inFarGrid := func(what string, got *gridwellv1.Tile) *gridwellv1.Tile {
		t.Helper()
		g, err := h.localCl.GetGrid(ctx, farGrid)
		if err != nil {
			t.Fatalf("%s: read back %s: %v", what, farGrid, err)
		}
		for _, tl := range g.Tiles {
			if tl.Id == got.Id {
				return tl
			}
		}
		t.Fatalf("%s: %s not in %s", what, got.Id, farGrid)
		return nil
	}

	farDoor, err := h.localCl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: farGrid, Tile: &gridwellv1.Tile{Kind: rpc.KindDoor, X: 0, Y: 0, W: 1, H: 1, ChildGridId: farRoot}})
	if err != nil {
		t.Fatalf("create door with a qualified child: %v", err)
	}
	for _, tl := range []*gridwellv1.Tile{farDoor, inFarGrid("door", farDoor)} {
		if tl.ChildGridId != farRoot || !tl.Link {
			t.Fatalf("door child = %q link=%v, want %q as a link", tl.ChildGridId, tl.Link, farRoot)
		}
	}
	if _, err := h.localCl.GetGrid(ctx, farDoor.ChildGridId); err != nil {
		t.Fatalf("the door's child does not resolve: %v", err)
	}

	link, err := h.localCl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: farGrid, Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 2, Y: 0, W: 1, H: 1, LinkTargetId: target.Id}})
	if err != nil {
		t.Fatalf("create leaf link: %v", err)
	}
	for _, tl := range []*gridwellv1.Tile{link, inFarGrid("link", link)} {
		if tl.LinkTargetId != target.Id || !tl.Link {
			t.Fatalf("link target = %q link=%v, want %q as a link", tl.LinkTargetId, tl.Link, target.Id)
		}
	}
	if data, _, _, err := h.localCl.ReadContent(ctx, link.Id); err != nil || string(data) != string(body) {
		t.Fatalf("read through the far link = %q (%v), want %q", data, err, body)
	}

	// A cross-namespace copy writes the copy straight into the far grid.
	homeLink, err := h.localCl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: localHome, Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 0, Y: 0, W: 1, H: 1, LinkTargetId: target.Id}})
	if err != nil {
		t.Fatalf("create home link: %v", err)
	}
	copied, err := h.localCl.CopyTile(ctx, &gridwellv1.CopyTileRequest{TileId: homeLink.Id, DestGridId: farGrid, X: 4, Y: 0})
	if err != nil {
		t.Fatalf("copy a home link into the far grid: %v", err)
	}
	for _, tl := range []*gridwellv1.Tile{copied, inFarGrid("copied link", copied)} {
		if tl.LinkTargetId != target.Id || !tl.Link {
			t.Fatalf("copied link target = %q link=%v, want %q", tl.LinkTargetId, tl.Link, target.Id)
		}
	}

	// The secondary ids of placement and a same-namespace copy.
	placed, err := h.localCl.MoveTile(ctx, claimtest.Claimed(t, h.localCl, &gridwellv1.MoveTileRequest{TileId: target.Id, GridId: farGrid, X: 0, Y: 4, W: 1, H: 1}))
	if err != nil {
		t.Fatalf("place into the far grid: %v", err)
	}
	if placed.GridId != farGrid || inFarGrid("placed", placed).GridId != farGrid {
		t.Fatalf("placed grid = %q, want %q", placed.GridId, farGrid)
	}
	cp, err := h.localCl.CopyTile(ctx, &gridwellv1.CopyTileRequest{TileId: target.Id, DestGridId: farGrid, X: 2, Y: 4})
	if err != nil {
		t.Fatalf("copy within the far node: %v", err)
	}
	if cp.GridId != farGrid || inFarGrid("copy", cp).GridId != farGrid {
		t.Fatalf("copy grid = %q, want %q", cp.GridId, farGrid)
	}
}
