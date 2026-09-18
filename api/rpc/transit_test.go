package rpc

import (
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// The transit grid rule lives once, in TransitQualifyGrid, so a new
// id-bearing Grid field cannot reach one hop and miss the other. Ids gain
// one segment, the far node's stamped facts ride verbatim, and the input is
// never mutated.
func TestTransitQualifyGrid(t *testing.T) {
	in := &pb.Grid{
		Id:            "far/7",
		ScratchGridId: "far/9",
		NodeNs:        "farnode",
		Writable:      true,
		HostContent:   true,
		Glyph:         "folder",
		MenuEntries:   []*pb.MenuEntry{{Id: "search", GridId: "far/7"}},
	}
	out := TransitQualifyGrid("hop", in)
	if out.Id != "hop/far/7" || out.ScratchGridId != "hop/far/9" || out.NodeNs != "hop/farnode" {
		t.Fatalf("ids not prepended one segment: %+v", out)
	}
	if !out.Writable || !out.HostContent || out.Glyph != "folder" {
		t.Fatalf("stamped facts must ride verbatim: %+v", out)
	}
	if len(out.MenuEntries) != 1 || out.MenuEntries[0].GridId != "hop/far/7" {
		t.Fatalf("menu entry roots not prepended: %+v", out.MenuEntries)
	}
	if in.Id != "far/7" || in.MenuEntries[0].GridId != "far/7" {
		t.Fatalf("input mutated: %+v", in)
	}
	if TransitQualifyGrid("hop", nil) != nil {
		t.Fatal("nil grid must stay nil")
	}
	if got := TransitQualifyGrid("hop", &pb.Grid{Id: "far/7"}); got.ScratchGridId != "" {
		t.Fatalf("empty scratch id must stay empty, got %q", got.ScratchGridId)
	}
}

// A node built before the connections list was retired still answers with
// one. The transit hop is its one reader: every row it carries joins plugins
// in ConnectionRow's shape, qualified like the rows beside it, after them.
func TestTransitQualifyPluginListFoldsARetiredConnectionsList(t *testing.T) {
	out := TransitQualifyPluginList("hop", &pb.HandshakeResponse{
		Plugins: []*pb.PluginInfo{{Uuid: "n1", Kind: "home", RootGridId: "n1/1"}},
		Connections: []*pb.ConnectionInfo{
			{Uuid: "n1/rtb", Label: "rtb", RootGridId: "n1/rtb/r/1", RootViewCx: 2, RootViewCy: 3, RootViewZoom: 0.5},
			{Uuid: "n1/far", Label: "far", StatusDetail: "dial refused"},
		},
	})
	if len(out.Connections) != 0 {
		t.Fatalf("the retired list must not be forwarded, got %+v", out.Connections)
	}
	if len(out.Plugins) != 3 || out.Plugins[0].Uuid != "hop/n1" {
		t.Fatalf("plugins = %+v, want home then the two connection rows", out.Plugins)
	}
	var rows []*pb.PluginInfo
	for _, pl := range out.Plugins {
		if IsConnectionRow(pl) {
			rows = append(rows, pl)
		}
	}
	want := []*pb.PluginInfo{
		ConnectionRow("hop/n1/rtb", "rtb", "hop/n1/rtb/r/1", "", Framing{Cx: 2, Cy: 3, Zoom: 0.5}),
		ConnectionRow("hop/n1/far", "far", "", "dial refused", Framing{}),
	}
	if len(rows) != len(want) {
		t.Fatalf("connection rows = %+v, want %+v", rows, want)
	}
	for i := range want {
		if !proto.Equal(rows[i], want[i]) {
			t.Errorf("row %d = %+v, want %+v", i, rows[i], want[i])
		}
	}
}
