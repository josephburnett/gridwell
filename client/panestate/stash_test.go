package panestate

import (
	"reflect"
	"testing"

	"github.com/josephburnett/gridwell/client/pane"
)

// Stash then restore is the identity on the descent fields — the pair is
// one list, so a field one side forgets fails here.
func TestStashRestoreDescentRoundTrip(t *testing.T) {
	src := &pane.Pane{ID: "p1", Anchor: "u1/0", Path: []string{"w1", "w2"},
		TextFocus: "t9", TextMode: "raw", TextScrollX: 3, TextScrollY: 4}
	var s Saved
	s.StashDescent(src)
	dst := &pane.Pane{ID: "p1"}
	if !s.RestoreDescent(dst) {
		t.Fatal("a stash with a focus must restore")
	}
	if !reflect.DeepEqual(src, dst) {
		t.Fatalf("round trip lost a field:\n got %+v\nwant %+v", dst, src)
	}
	src.Path[0] = "changed"
	if s.Path[0] == "changed" || dst.Path[0] == "changed" {
		t.Fatal("the stash must own its path slice")
	}
}

func TestRestoreDescentNoStash(t *testing.T) {
	p := &pane.Pane{ID: "p1", Anchor: "keep", TextFocus: ""}
	s := Saved{Cx: 1, Cy: 2, Zoom: 3} // an ordinary ascent
	if s.RestoreDescent(p) || p.Anchor != "keep" {
		t.Fatal("no stashed focus: nothing restored")
	}
	s = Saved{TextFocus: "t1"} // same-grid stash: the pane keeps its anchor
	if !s.RestoreDescent(p) || p.Anchor != "keep" || p.TextFocus != "t1" {
		t.Fatalf("same-grid stash: %+v", p)
	}
}
