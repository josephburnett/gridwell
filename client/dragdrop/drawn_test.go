package dragdrop

import (
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"

	"github.com/josephburnett/gridwell/api/rpc"
)

// A copy is drawn at the release as what rpc.CopyOf makes of it, the ghost's
// promise: a Gridwell tile as itself with nothing the node mints, and
// whatever copies as a link, dashed, naming what the link will.
func TestACopyIsDrawnAsWhatTheNodeWillMakeOfIt(t *testing.T) {
	const node, far = "nhome01", "nhome01/far/nfar001"
	dir := rpc.QualifyID(node, rpc.KeyTileID("/tmp/d"))
	cases := []struct {
		name string
		row  *gridwellv1.Tile
		dest string
		want *gridwellv1.Tile
	}{
		{name: "a text as itself, without its bytes",
			row:  &gridwellv1.Tile{Id: node + "/3", GridId: node + "/1", Kind: rpc.KindText, AltText: "notes", W: 2, H: 1, BlobId: 9, Version: 4},
			dest: node + "/1",
			want: &gridwellv1.Tile{GridId: node + "/1", Kind: rpc.KindText, AltText: "notes", X: 5, Y: 6, W: 2, H: 1}},
		{name: "a door as a door, its grid the node's to mint",
			row:  &gridwellv1.Tile{Id: node + "/4", Kind: rpc.KindDoor, ChildGridId: node + "/8", AltText: "lobby", Named: true, W: 1, H: 1},
			dest: node + "/1",
			want: &gridwellv1.Tile{GridId: node + "/1", Kind: rpc.KindDoor, AltText: "lobby", Named: true, X: 5, Y: 6, W: 1, H: 1}},
		{name: "a shell onto a far node as a link to it",
			row:  &gridwellv1.Tile{Id: node + "/5", Kind: rpc.KindShell, W: 1, H: 1},
			dest: far + "/2",
			want: &gridwellv1.Tile{GridId: far + "/2", Kind: rpc.KindShell, LinkTargetId: node + "/5", Link: true, X: 5, Y: 6, W: 1, H: 1}},
		{name: "a directory's door as a link to its grid",
			row:  &gridwellv1.Tile{Id: dir, Kind: rpc.KindDoor, ChildGridId: dir, W: 1, H: 1},
			dest: node + "/1",
			want: &gridwellv1.Tile{GridId: node + "/1", Kind: rpc.KindDoor, ChildGridId: dir, Link: true, X: 5, Y: 6, W: 1, H: 1}},
		{name: "a link as another link to its target",
			row:  &gridwellv1.Tile{Id: node + "/6", Kind: rpc.KindText, LinkTargetId: node + "/3", Link: true, W: 1, H: 1},
			dest: node + "/1",
			want: &gridwellv1.Tile{GridId: node + "/1", Kind: rpc.KindText, LinkTargetId: node + "/3", Link: true, X: 5, Y: 6, W: 1, H: 1}},
		{name: "nothing for a copy the node refuses",
			row:  &gridwellv1.Tile{Id: node + "/7", Kind: "nonsense"},
			dest: node + "/1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CopyDrawn(tc.row, tc.dest, true, true, 5, 6)
			if (got == nil) != (tc.want == nil) || got != nil && got.String() != tc.want.String() {
				t.Errorf("CopyDrawn = %v\nwant %v", got, tc.want)
			}
		})
	}
}

// A create is drawn as the row it asks for on its grid, dashed when it names
// a target, a link's rpc.LinkOf row among them; a name made on disk is the
// files' to draw.
func TestACreateIsDrawnAsTheRowItAsksFor(t *testing.T) {
	src := &gridwellv1.Tile{Id: "nhome01/3", Kind: rpc.KindText, AltText: "notes", W: 1, H: 2}
	door := &gridwellv1.Tile{Id: "nhome01/4", Kind: rpc.KindDoor, ChildGridId: "nhome01/9", W: 1, H: 1}
	cases := []struct {
		name string
		tile *gridwellv1.Tile
		file bool
		link bool
	}{
		{name: "a new door", tile: &gridwellv1.Tile{Kind: rpc.KindDoor, X: 4, W: 1, H: 1}},
		{name: "a link to a text", tile: rpc.LinkOf(src, 4, 0), link: true},
		{name: "a link to a door's grid", tile: rpc.LinkOf(door, 4, 0), link: true},
		{name: "a file", tile: &gridwellv1.Tile{Kind: rpc.KindText, AltText: "a.txt"}, file: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CreateDrawn(&gridwellv1.CreateTileRequest{GridId: "nhome01/1", Tile: tc.tile, File: tc.file})
			if tc.file {
				if got != nil {
					t.Errorf("CreateDrawn of a file = %v, want nil", got)
				}
				return
			}
			if got.GridId != "nhome01/1" || got.Link != tc.link || got.X != tc.tile.X || got.Kind != tc.tile.Kind {
				t.Errorf("CreateDrawn = %v, want the asked row on its grid, link %v", got, tc.link)
			}
			if tc.tile.GridId != "" || tc.tile.Link {
				t.Errorf("CreateDrawn wrote the request's row")
			}
		})
	}
}
