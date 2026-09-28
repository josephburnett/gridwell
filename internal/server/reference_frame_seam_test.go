package server_test

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// Every link is an id the holding node resolves from its first segment. A
// reference written into a far grid is stored in that node's frame, spelled
// along its own connections, or refused with nothing stored.
func TestAReferenceIsWrittenInTheHoldersFrameOrRefused(t *testing.T) {
	ctx := context.Background()
	edges := append([]meshEdge{{aNode, "tob2", bNode}}, branches...)
	m := newMesh(t, []string{meNode, aNode, bNode, cNode}, edges)
	me := m.nodes[meNode].cl
	aHome, bHome := m.home(meNode, aNode, "toa"), m.home(meNode, bNode, "tob")
	cText := m.text(ctx, meNode, m.home(meNode, cNode, "toa", "toc"), 0, "on c")
	bText := m.text(ctx, meNode, bHome, 0, "on b")
	aText := m.text(ctx, meNode, aHome, 0, "on a")
	homeText := m.text(ctx, meNode, rpc.QualifyID(meNode, m.nodes[meNode].root), 0, "on me")

	link := func(grid string, x int64, target string) (*gridwellv1.Tile, error) {
		return me.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: grid,
			Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: x, Y: 2, W: 1, H: 1, LinkTargetId: target}})
	}
	wantLink := func(t *testing.T, got *gridwellv1.Tile, grid, target, body string) {
		t.Helper()
		for _, tl := range []*gridwellv1.Tile{got, m.tileIn(ctx, meNode, grid, got.Id)} {
			if tl.LinkTargetId != target || !tl.Reference {
				t.Fatalf("link target = %q reference=%v, want %q", tl.LinkTargetId, tl.Reference, target)
			}
		}
		if b := readBody(ctx, t, me, got.Id); b != body {
			t.Fatalf("read through the link = %q, want %q", b, body)
		}
	}
	wantRefused := func(t *testing.T, err error, grid string, before int) {
		t.Helper()
		if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "has no connection to") {
			t.Fatalf("err = %v, want InvalidArgument naming the missing connection", err)
		}
		if n := m.count(ctx, meNode, grid); n != before {
			t.Fatalf("%s holds %d tiles after a refusal, want %d", grid, n, before)
		}
	}

	t.Run("beyond the holder on the same path", func(t *testing.T) {
		got, err := link(aHome, 1, cText.Id)
		if err != nil {
			t.Fatal(err)
		}
		wantLink(t, got, aHome, cText.Id, "on c")
	})

	t.Run("another branch the holder reaches", func(t *testing.T) {
		got, err := link(aHome, 2, bText.Id)
		if err != nil {
			t.Fatal(err)
		}
		spelled := strings.Replace(bText.Id, meNode+"/tob/", meNode+"/toa/"+aNode+"/tob2/", 1)
		wantLink(t, got, aHome, spelled, "on b")
		// The holder's own door reads its own spelling.
		onA := m.tileIn(ctx, aNode, rpc.QualifyID(aNode, m.nodes[aNode].root), strings.TrimPrefix(got.Id, meNode+"/toa/"))
		if want := strings.TrimPrefix(spelled, meNode+"/toa/"); onA.LinkTargetId != want {
			t.Fatalf("on a the link names %q, want %q", onA.LinkTargetId, want)
		}

		well, err := me.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: aHome,
			Tile: &gridwellv1.Tile{Kind: rpc.KindWell, X: 3, Y: 2, W: 1, H: 1, ChildGridId: bHome}})
		if err != nil {
			t.Fatal(err)
		}
		if want := strings.Replace(bHome, meNode+"/tob/", meNode+"/toa/"+aNode+"/tob2/", 1); well.ChildGridId != want {
			t.Fatalf("well child = %q, want %q", well.ChildGridId, want)
		}
		if _, err := me.GetGrid(ctx, well.ChildGridId); err != nil {
			t.Fatalf("the well's child does not resolve: %v", err)
		}

		// A copied link is written the same way.
		homeLink, err := link(rpc.QualifyID(meNode, m.nodes[meNode].root), 4, bText.Id)
		if err != nil {
			t.Fatal(err)
		}
		copied, err := me.CloneTile(ctx, &gridwellv1.CloneTileRequest{TileId: homeLink.Id, DestGridId: aHome, X: 4, Y: 2})
		if err != nil {
			t.Fatalf("clone a home link into a: %v", err)
		}
		wantLink(t, copied, aHome, spelled, "on b")
	})

	t.Run("another branch the holder cannot reach", func(t *testing.T) {
		before := m.count(ctx, meNode, bHome)
		_, err := link(bHome, 1, aText.Id)
		wantRefused(t, err, bHome, before)
		_, err = me.CloneTile(ctx, &gridwellv1.CloneTileRequest{TileId: mustLink(ctx, t, me, m, aText.Id).Id, DestGridId: bHome, X: 1, Y: 2})
		wantRefused(t, err, bHome, before)
	})

	t.Run("this node's home with no way back", func(t *testing.T) {
		before := m.count(ctx, meNode, aHome)
		_, err := link(aHome, 5, homeText.Id)
		wantRefused(t, err, aHome, before)
	})
}

// mustLink is a leaf link in me's home, where any id of me's frame is held.
func mustLink(ctx context.Context, t *testing.T, me *rpc.Client, m *mesh, target string) *gridwellv1.Tile {
	t.Helper()
	l, err := me.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: rpc.QualifyID(meNode, m.nodes[meNode].root),
		Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 9, Y: 9, W: 1, H: 1, LinkTargetId: target}})
	if err != nil {
		t.Fatal(err)
	}
	return l
}
