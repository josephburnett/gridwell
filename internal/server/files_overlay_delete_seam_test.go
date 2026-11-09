package server_test

// Deleting a directory within Gridwell sends it to the OS trash and its
// Gridwell tiles to the Gridwell trash (CLAUDE.md, 2026-11-08).

import (
	"path/filepath"
	"slices"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// Deleting a directory sends it to the OS trash and every Gridwell tile
// under it to the Gridwell trash, a door with its grid; one Gridwell tile on
// a directory goes to the Gridwell trash alone.
func TestDeletingADirectoryTrashesItsOverlay(t *testing.T) {
	root := t.TempDir()
	d, e := filepath.Join(root, "d"), filepath.Join(root, "d", "e")
	writeFile(t, filepath.Join(e, "f.txt"), "f")
	n := newFilesNode(t, root)
	top := n.overText(fileID(d), 3, 3, "top")
	alone := n.overText(fileID(d), 4, 3, "alone")
	door, err := n.cl.CreateTile(n.ctx, &gridwellv1.CreateTileRequest{GridId: fileID(e),
		Tile: &gridwellv1.Tile{Kind: rpc.KindDoor, X: 2, Y: 2, W: 1, H: 1, AltText: "Notes"}})
	if err != nil {
		t.Fatal(err)
	}
	inner := n.overText(door.ChildGridId, 0, 0, "inner")
	if _, err := n.cl.DeleteTile(n.ctx, &gridwellv1.DeleteTileRequest{TileId: alone.Id, ClaimVersion: alone.Version, Claim: rpc.PlacementOf(alone)}); err != nil {
		t.Fatal(err)
	}
	if !n.inTrash(alone.Id) {
		t.Error("one Gridwell tile deleted from a directory is not in the Gridwell trash")
	}
	events := n.show(n.roots[0], fileID(d))
	dt := n.tile(n.roots[0], d)
	if _, err := n.cl.DeleteTile(n.ctx, &gridwellv1.DeleteTileRequest{TileId: dt.Id, ClaimVersion: dt.Version, Claim: rpc.PlacementOf(dt)}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(n.trashed, d) {
		t.Errorf("trashed %v, want d in the OS trash", n.trashed)
	}
	await(t, events, "the top text leaving d", func(ev *gridwellv1.Event) bool {
		tl := ev.GetTileChanged().GetTile()
		return tl.GetId() == top.Id && !rpc.IsFilesID(tl.GetGridId())
	})
	for _, tl := range []*gridwellv1.Tile{top, door} {
		if !n.inTrash(tl.Id) {
			t.Errorf("%s (%s) is not in the Gridwell trash", tl.Id, tl.Kind)
		}
	}
	if got, err := n.cl.GetTile(n.ctx, inner.Id); err != nil || got.GridId != door.ChildGridId {
		t.Errorf("the door's own text reads %v, %v; want it still in the door's grid", got, err)
	}
	if got := listed(n.grid(n.roots[0]), fileID(d)); got != nil {
		t.Errorf("the root still lists d as %v", got)
	}
}
