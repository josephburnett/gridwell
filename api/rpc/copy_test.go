package rpc

import (
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// TestCopyOf is the table of what a copy of each kind becomes, every row a
// decision in CLAUDE.md, for the router's walk and the store's copy alike.
func TestCopyOf(t *testing.T) {
	md := "n/" + KeyTileID("/r/notes.md")
	png := "n/" + KeyTileID("/r/cat.png")
	cases := []struct {
		name       string
		tile       *pb.Tile
		sameSource bool
		accepts    bool
		want       CopyAs
		target     string
	}{
		{"leaf link links to its target", &pb.Tile{Id: "n/1", Kind: KindText, LinkTargetId: "m/9", Link: true}, true, true, CopyLinkTile, "m/9"},
		{"leaf link across sources too", &pb.Tile{Id: "n/1", Kind: KindURL, LinkTargetId: "m/9", Link: true}, false, true, CopyLinkTile, "m/9"},
		{"door that is a link links to its grid", &pb.Tile{Id: "n/1", Kind: KindDoor, ChildGridId: "m/4", Link: true}, true, true, CopyLinkGrid, "m/4"},
		{"door whose grid takes no tiles links", &pb.Tile{Id: "n/1", Kind: KindDoor, ChildGridId: "n/4"}, false, false, CopyLinkGrid, "n/4"},
		{"solid door copies deep", &pb.Tile{Id: "n/1", Kind: KindDoor, ChildGridId: "n/4"}, false, true, CopyDeep, "n/4"},
		{"solid door copies deep within a source", &pb.Tile{Id: "1", Kind: KindDoor, ChildGridId: "4"}, true, true, CopyDeep, "4"},
		{"shell across sources links to itself", &pb.Tile{Id: "n/1", Kind: KindShell}, false, true, CopyLinkTile, "n/1"},
		{"shell within a source names its tmux", &pb.Tile{Id: "1", Kind: KindShell}, true, true, CopyShell, "1"},
		{"a copy of a copy names the first tmux", &pb.Tile{Id: "2", Kind: KindShell, ShellTmux: "1"}, true, true, CopyShell, "1"},
		{"text copies as a leaf", &pb.Tile{Id: "n/1", Kind: KindText}, false, true, CopyLeaf, ""},
		{"url copies as a leaf", &pb.Tile{Id: "n/1", Kind: KindURL}, true, true, CopyLeaf, ""},
		{"workspace copies as a leaf", &pb.Tile{Id: "n/1", Kind: KindWorkspace}, false, true, CopyLeaf, ""},
		{"markdown file copies its bytes", &pb.Tile{Id: md, Kind: KindText}, false, true, CopyLeaf, ""},
		{"any other file links", &pb.Tile{Id: png, Kind: KindURL}, false, true, CopyLinkTile, png},
		{"an unknown kind is refused", &pb.Tile{Id: "n/1", Kind: "pond"}, false, true, CopyRefused, `unsupported tile kind "pond"`},
	}
	for _, c := range cases {
		asked := false
		as, target := CopyOf(c.tile, c.sameSource, func() bool { asked = true; return c.accepts })
		if as != c.want || target != c.target {
			t.Errorf("%s: CopyOf = %v %q, want %v %q", c.name, as, target, c.want, c.target)
		}
		// Only a door that is not a link needs its grid read.
		if wantAsk := IsDoorKind(c.tile.Kind) && !c.tile.Link; asked != wantAsk {
			t.Errorf("%s: accepts asked = %v, want %v", c.name, asked, wantAsk)
		}
	}
}
