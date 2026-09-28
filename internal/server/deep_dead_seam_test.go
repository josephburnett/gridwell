package server_test

import (
	"context"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
)

// A link broken at any hop reads as dead, the answer the client shows as the
// dead state rather than an error; a hop that is declared but does not answer
// is dark, not dead; and a hop declared again brings the link back as it was.
func TestALinkBrokenAtAnyHopReadsDead(t *testing.T) {
	ctx := context.Background()
	m := newMesh(t, []string{meNode, aNode, cNode}, []meshEdge{
		{meNode, "toa", aNode},
		{aNode, "toc", cNode},
	})
	me := m.nodes[meNode].cl
	target := m.text(ctx, meNode, m.home(meNode, cNode, "toa", "toc"), 0, "two hops away")
	link, err := me.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: rpc.QualifyID(meNode, m.nodes[meNode].root),
		Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 0, Y: 0, W: 1, H: 1, LinkTargetId: target.Id}})
	if err != nil {
		t.Fatal(err)
	}
	room := m.home(meNode, cNode, "toa", "toc")

	wantDead := func(t *testing.T, why string) {
		t.Helper()
		if _, _, _, err := me.ReadContent(ctx, link.Id); !gwerr.IsDeadRef(err) {
			t.Fatalf("%s: read through the link = %v, want the dead verdict", why, err)
		}
		if _, err := me.GetGrid(ctx, room); !gwerr.IsDeadRef(err) {
			t.Fatalf("%s: read of the far grid = %v, want the dead verdict", why, err)
		}
		if _, err := me.GetTile(ctx, target.Id); !gwerr.IsDeadRef(err) {
			t.Fatalf("%s: read of the far tile = %v, want the dead verdict", why, err)
		}
	}
	wantLive := func(t *testing.T, why string) {
		t.Helper()
		if b := readBody(ctx, t, me, link.Id); b != "two hops away" {
			t.Fatalf("%s: read through the link = %q", why, b)
		}
		if got := m.tileIn(ctx, meNode, rpc.QualifyID(meNode, m.nodes[meNode].root), link.Id); got.LinkTargetId != target.Id {
			t.Fatalf("%s: the link names %q, want %q unchanged", why, got.LinkTargetId, target.Id)
		}
	}

	wantLive(t, "declared")

	m.redeclare(aNode, nil, nil)
	wantDead(t, "middle hop undeclared")

	m.redeclare(aNode, []meshEdge{{aNode, "toc", "nowhere"}}, nil)
	if _, _, _, err := me.ReadContent(ctx, link.Id); err == nil || gwerr.IsDeadRef(err) {
		t.Fatalf("a declared hop that does not answer = %v, want an error that is not the dead verdict", err)
	}

	m.redeclare(aNode, []meshEdge{{aNode, "toc", cNode}}, nil)
	wantLive(t, "declared again")

	m.redeclare(aNode, nil, []string{"toc"})
	wantDead(t, "middle hop retired")
}
