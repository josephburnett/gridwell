package server_test

// A directory gone underneath Gridwell stays as a gone door (CLAUDE.md,
// 2026-11-08).

import (
	"os"
	"path/filepath"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// A directory gone underneath Gridwell stays in its parent as a gone door
// holding its Gridwell tiles, its parent gone too and listed by the
// grandparent; moving the last tile out takes the door away, an open view
// hearing it; deleting a gone door trashes its tiles; and the path made
// again is an ordinary directory with its overlay; a gone directory with no
// Gridwell tile left is dead.
func TestAGoneDirectoryStaysAsAGoneDoor(t *testing.T) {
	root := t.TempDir()
	g, h := filepath.Join(root, "g"), filepath.Join(root, "g", "h")
	writeFile(t, filepath.Join(h, "f.txt"), "f")
	n := newFilesNode(t, root)
	kept := n.overText(fileID(h), 1, 1, "kept")
	if err := os.RemoveAll(g); err != nil {
		t.Fatal(err)
	}
	door := listed(n.grid(n.roots[0]), fileID(g))
	if door == nil || !door.Gone || door.Kind != rpc.KindDoor || door.AltText != "g" || door.ChildGridId != fileID(g) {
		t.Fatalf("the root lists g as %v, want a gone door onto its grid", door)
	}
	mid := n.grid(fileID(g))
	if hd := listed(mid, fileID(h)); hd == nil || !hd.Gone || len(mid.Tiles) != 1 {
		t.Errorf("the gone g lists %v, want h's gone door alone", mid.Tiles)
	}
	gh := n.grid(fileID(h))
	if gh.Grid.Writable || !gh.Grid.GetAcceptsTiles() || len(gh.Tiles) != 1 || gh.Tiles[0].Id != kept.Id {
		t.Errorf("the gone h answers %v with %v, want its text alone, not writable, taking tiles", gh.Grid, gh.Tiles)
	}
	if err := os.MkdirAll(h, 0o755); err != nil {
		t.Fatal(err)
	}
	if door := listed(n.grid(n.roots[0]), fileID(g)); door == nil || door.Gone {
		t.Errorf("g made again is listed as %v, want an ordinary door", door)
	}
	if got := listed(n.grid(fileID(h)), kept.Id); got == nil || got.X != 1 {
		t.Errorf("h made again lists its text as %v", got)
	}
	if err := os.RemoveAll(g); err != nil {
		t.Fatal(err)
	}
	events := n.show(n.roots[0])
	got := n.grid(fileID(h)).Tiles[0]
	if _, err := n.cl.MoveTile(n.ctx, &gridwellv1.MoveTileRequest{TileId: got.Id, GridId: n.home, X: 9, Y: 9, W: 1, H: 1,
		Claim: rpc.PlacementOf(got)}); err != nil {
		t.Fatal(err)
	}
	await(t, events, "the root's change", func(ev *gridwellv1.Event) bool {
		return ev.GetGridChanged().GetGridId() == n.roots[0]
	})
	if door := listed(n.grid(n.roots[0]), fileID(g)); door != nil {
		t.Errorf("g with no Gridwell tile left is listed as %v", door)
	}
	if _, err := n.cl.GetGrid(n.ctx, fileID(h)); err == nil {
		t.Error("the gone h with no Gridwell tile left still answers")
	}
	k := filepath.Join(root, "k")
	if err := os.Mkdir(k, 0o755); err != nil {
		t.Fatal(err)
	}
	back := n.overText(fileID(k), 2, 2, "back")
	if err := os.Remove(k); err != nil {
		t.Fatal(err)
	}
	gd := listed(n.grid(n.roots[0]), fileID(k))
	if gd == nil || !gd.Gone {
		t.Fatalf("the root lists the gone k as %v", gd)
	}
	if _, err := n.cl.DeleteTile(n.ctx, &gridwellv1.DeleteTileRequest{TileId: gd.Id, ClaimVersion: gd.Version, Claim: rpc.PlacementOf(gd)}); err != nil {
		t.Fatal(err)
	}
	if !n.inTrash(back.Id) {
		t.Error("deleting the gone door left its text out of the Gridwell trash")
	}
	if door := listed(n.grid(n.roots[0]), fileID(k)); door != nil {
		t.Errorf("the deleted gone door is still listed as %v", door)
	}
}
