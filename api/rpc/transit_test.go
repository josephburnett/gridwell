package rpc

import "testing"

import pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"

// The transit grid rule lives ONCE (TransitQualifyGrid) — before the
// extraction the server's hop and the builtin transport's hop each kept
// a hand copy, and a new id-bearing Grid field added to one could
// silently miss the other. Ids gain one segment; the far node's stamped
// facts ride verbatim; the input is never mutated.
func TestTransitQualifyGrid(t *testing.T) {
	in := &pb.Grid{
		Id:            "far/7",
		ScratchGridId: "far/9",
		NodeNs:        "farnode",
		Writable:      true,
		SourceKind:    "fs",
		MenuEntries:   []*pb.MenuEntry{{Id: "search", GridId: "far/7"}},
	}
	out := TransitQualifyGrid("hop", in)
	if out.Id != "hop/far/7" || out.ScratchGridId != "hop/far/9" || out.NodeNs != "hop/farnode" {
		t.Fatalf("ids not prepended one segment: %+v", out)
	}
	if !out.Writable || out.SourceKind != "fs" {
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
