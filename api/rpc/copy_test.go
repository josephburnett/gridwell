package rpc

import (
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// TestCopyOf is the table of what a copy of each kind becomes, every row a
// decision in CLAUDE.md, for the router's walk, the store's copy and the drop
// verdict alike.
func TestCopyOf(t *testing.T) {
	md := "n/" + KeyTileID("/r/notes.md")
	png := "n/" + KeyTileID("/r/cat.png")
	dir := "n/" + KeyTileID("/r/sub")
	into := "n/" + KeyTileID("/r/other")
	cases := []struct {
		name     string
		tile     *pb.Tile
		dest     string
		sameNode bool
		accepts  bool
		want     CopyAs
		target   string
	}{
		{"leaf link links to its target", &pb.Tile{Id: "n/1", Kind: KindText, LinkTargetId: "m/9", Link: true}, "n/2", true, true, CopyLinkTile, "m/9"},
		{"leaf link across sources too", &pb.Tile{Id: "n/1", Kind: KindURL, LinkTargetId: "m/9", Link: true}, "m/2", false, true, CopyLinkTile, "m/9"},
		{"door that is a link links to its grid", &pb.Tile{Id: "n/1", Kind: KindDoor, ChildGridId: "m/4", Link: true}, "n/2", true, true, CopyLinkGrid, "m/4"},
		{"door whose grid takes no tiles links", &pb.Tile{Id: "n/1", Kind: KindDoor, ChildGridId: "n/4"}, "m/2", false, false, CopyLinkGrid, "n/4"},
		{"solid door copies deep", &pb.Tile{Id: "n/1", Kind: KindDoor, ChildGridId: "n/4"}, "m/2", false, true, CopyDeep, "n/4"},
		{"solid door copies deep within a source", &pb.Tile{Id: "1", Kind: KindDoor, ChildGridId: "4"}, "2", true, true, CopyDeep, "4"},
		{"a Gridwell door copies deep into a directory", &pb.Tile{Id: "n/1", Kind: KindDoor, ChildGridId: "n/4"}, into, true, true, CopyDeep, "n/4"},
		{"shell across sources links to itself", &pb.Tile{Id: "n/1", Kind: KindShell}, "m/2", false, true, CopyLinkTile, "n/1"},
		{"shell within a source names its tmux", &pb.Tile{Id: "1", Kind: KindShell}, "2", true, true, CopyShell, "1"},
		{"shell into a directory of its node names its tmux", &pb.Tile{Id: "n/1", Kind: KindShell}, into, true, true, CopyShell, "n/1"},
		{"a copy of a copy names the first tmux", &pb.Tile{Id: "2", Kind: KindShell, ShellTmux: "1"}, "3", true, true, CopyShell, "1"},
		{"text copies as a leaf", &pb.Tile{Id: "n/1", Kind: KindText}, "m/2", false, true, CopyLeaf, ""},
		{"text copies as a leaf into a directory", &pb.Tile{Id: "n/1", Kind: KindText}, into, true, true, CopyLeaf, ""},
		{"url copies as a leaf", &pb.Tile{Id: "n/1", Kind: KindURL}, "n/2", true, true, CopyLeaf, ""},
		{"workspace copies as a leaf", &pb.Tile{Id: "n/1", Kind: KindWorkspace}, "m/2", false, true, CopyLeaf, ""},
		{"a markdown file onto a Gridwell grid links", &pb.Tile{Id: md, Kind: KindText}, "n/2", true, true, CopyLinkTile, md},
		{"a page file onto a Gridwell grid links", &pb.Tile{Id: png, Kind: KindURL}, "n/2", true, true, CopyLinkTile, png},
		{"a file onto a far grid links", &pb.Tile{Id: md, Kind: KindText}, "m/2", false, true, CopyLinkTile, md},
		{"a file into a directory of its node copies on disk", &pb.Tile{Id: png, Kind: KindURL}, into, true, true, CopyFile, ""},
		{"a file into a far directory links", &pb.Tile{Id: md, Kind: KindText}, "m/" + KeyTileID("/r"), false, true, CopyLinkTile, md},
		{"a directory never copies", &pb.Tile{Id: dir, Kind: KindDoor, ChildGridId: dir}, into, true, true, CopyLinkGrid, dir},
		{"a directory onto a Gridwell grid links", &pb.Tile{Id: dir, Kind: KindDoor, ChildGridId: dir}, "n/2", true, true, CopyLinkGrid, dir},
		{"an unknown kind is refused", &pb.Tile{Id: "n/1", Kind: "pond"}, "m/2", false, true, CopyRefused, `unsupported tile kind "pond"`},
	}
	for _, c := range cases {
		asked := false
		as, target := CopyOf(c.tile, c.dest, c.sameNode, func() bool { asked = true; return c.accepts })
		if as != c.want || target != c.target {
			t.Errorf("%s: CopyOf = %v %q, want %v %q", c.name, as, target, c.want, c.target)
		}
		// Only a Gridwell door that is not a link needs its grid read.
		if wantAsk := IsDoorKind(c.tile.Kind) && !c.tile.Link && !OnDisk(c.tile.Id); asked != wantAsk {
			t.Errorf("%s: accepts asked = %v, want %v", c.name, asked, wantAsk)
		}
	}
}

// TestMoveRefusal is the one rule a move is refused by before either side is
// asked: across a connection, or a file onto a Gridwell grid.
func TestMoveRefusal(t *testing.T) {
	dir := "n/" + KeyTileID("/r/sub")
	file := "n/" + KeyTileID("/r/a.txt")
	cases := []struct {
		name, tile, grid, want string
	}{
		{"a Gridwell row within home", "n/1", "n/2", ""},
		{"a Gridwell row onto a directory", "n/1", dir, ""},
		{"a file into another directory", file, dir, ""},
		{"a move naming no grid", file, "", ""},
		{"a file onto a Gridwell grid", file, "n/2", MoveFileOver},
		{"a Gridwell row onto a far grid", "n/1", "n/far/2", MoveAcross},
		{"a far row onto home", "n/far/1", "n/2", MoveAcross},
		{"a file onto a far directory", file, "n/far/" + KeyTileID("/r"), MoveAcross},
	}
	for _, c := range cases {
		if got := MoveRefusal(&pb.MoveTileRequest{TileId: c.tile, GridId: c.grid}); got != c.want {
			t.Errorf("%s: MoveRefusal = %q, want %q", c.name, got, c.want)
		}
	}
}
