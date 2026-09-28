package server_test

import (
	"context"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

const (
	meNode = "nodeme1"
	aNode  = "nodea01"
	bNode  = "nodeb01"
	cNode  = "nodec01"
)

// branches is me with two connections to two machines, and a second hop from
// the first: me → toa → a → toc → c, and me → tob → b.
var branches = []meshEdge{
	{meNode, "toa", aNode},
	{meNode, "tob", bNode},
	{aNode, "toc", cNode},
}

func readBody(ctx context.Context, t *testing.T, cl *rpc.Client, id string) string {
	t.Helper()
	data, _, _, err := cl.ReadContent(ctx, id)
	if err != nil {
		t.Fatalf("read %s: %v", id, err)
	}
	return string(data)
}

// A clone runs at the nearest node that sees both ends: two ids behind two
// different connections are copied here, reading through one and writing
// through the other; two ids behind the same connection are the far node's
// to clone, however deep they sit.
func TestCloneRunsAtTheNearestNodeThatSeesBothEnds(t *testing.T) {
	ctx := context.Background()
	m := newMesh(t, []string{meNode, aNode, bNode, cNode}, branches)
	me := m.nodes[meNode].cl

	t.Run("across two connections", func(t *testing.T) {
		src := m.text(ctx, meNode, m.home(meNode, aNode, "toa"), 0, "from a")
		dst := m.home(meNode, bNode, "tob")
		got, err := me.CloneTile(ctx, &gridwellv1.CloneTileRequest{TileId: src.Id, DestGridId: dst, X: 3, Y: 3})
		if err != nil {
			t.Fatalf("clone a → b: %v", err)
		}
		if got.GridId != dst || m.tileIn(ctx, meNode, dst, got.Id) == nil {
			t.Fatalf("clone landed in %q, want %q", got.GridId, dst)
		}
		if body := readBody(ctx, t, me, got.Id); body != "from a" {
			t.Fatalf("clone body = %q, want the source's", body)
		}
	})

	t.Run("home to two hops away", func(t *testing.T) {
		src := m.text(ctx, meNode, rpc.QualifyID(meNode, m.nodes[meNode].root), 0, "from home")
		dst := m.home(meNode, cNode, "toa", "toc")
		got, err := me.CloneTile(ctx, &gridwellv1.CloneTileRequest{TileId: src.Id, DestGridId: dst, X: 3, Y: 3})
		if err != nil {
			t.Fatalf("clone home → c: %v", err)
		}
		if got.GridId != dst || readBody(ctx, t, me, got.Id) != "from home" {
			t.Fatalf("clone = %+v, want a copy in %q", got, dst)
		}
	})

	t.Run("both ends behind one far node", func(t *testing.T) {
		aHome := m.home(meNode, aNode, "toa")
		src := m.text(ctx, meNode, aHome, 5, "stays on a")
		room, err := me.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: aHome,
			Tile: &gridwellv1.Tile{Kind: rpc.KindWell, X: 6, Y: 0, W: 1, H: 1}})
		if err != nil {
			t.Fatal(err)
		}
		asked := m.asks(meNode, "toa")
		clones, creates, writes := asked.clones.Load(), asked.creates.Load(), asked.writes.Load()
		got, err := me.CloneTile(ctx, &gridwellv1.CloneTileRequest{TileId: src.Id, DestGridId: room.ChildGridId, X: 0, Y: 0})
		if err != nil {
			t.Fatalf("clone within a: %v", err)
		}
		if got.GridId != room.ChildGridId || readBody(ctx, t, me, got.Id) != "stays on a" {
			t.Fatalf("clone = %+v, want a copy in %q", got, room.ChildGridId)
		}
		if asked.clones.Load() != clones+1 || asked.creates.Load() != creates || asked.writes.Load() != writes {
			t.Fatalf("across toa: clones %d→%d creates %d→%d writes %d→%d; want one CloneTile the far node ran and nothing copied from here",
				clones, asked.clones.Load(), creates, asked.creates.Load(), writes, asked.writes.Load())
		}
	})
}
