package pane

import "testing"

// A pane hidden under a zoomed sibling has no rect, so any view computed for
// it is computed against nothing: its overtake is 0 and the intrinsic zoom it
// would write is 0, which erased a well's framing in the 2026-10-04 trace. It
// is no framing writer, on a grid or in a content descent, where the text
// scroll it would write carries the empty rect's width and height of 0.
func TestHiddenPaneIsNoFramingWriter(t *testing.T) {
	for _, content := range []bool{false, true} {
		tr := NewTree()
		tr.FocusedPane().Reset(Frame{GridID: "root"})
		tr.FocusedPane().SetView(0, 0, 1)
		other, err := tr.Split(Horizontal)
		if err != nil {
			t.Fatal(err)
		}
		f := Frame{Door: "well8", Content: content}
		f.SetView(0.43, 3.7, 2)
		other.Push(f)
		tr.ToggleZoom("p1")

		rects := Layout(tr, Rect{W: 1200, H: 800})
		var pgs []PaneGrid
		tr.Walk(func(p *Pane) {
			pgs = append(pgs, PaneGrid{PaneID: p.ID, GridID: p.Anchor() + "/" + p.Door})
		})
		writers := FramingWriters(pgs, "p1", rects)
		if !writers["p1"] {
			t.Errorf("content=%v: the zoomed pane is no writer: %+v", content, writers)
		}
		for id, writes := range writers {
			if _, laid := rects[id]; writes && !laid {
				t.Errorf("content=%v: pane %s has no rect under the zoomed pane but is a framing writer", content, id)
			}
		}
	}
}

// A restored pane adopts its owner row's view once; a view with no zoom is
// what an empty rect yielded, and adopting it left the pane at live zoom 0
// for the rest of the session (blank, and a wheel then turned its center
// NaN). Adopt takes only a frame with a view.
func TestAdoptRefusesAViewWithNoZoom(t *testing.T) {
	s := NewStack("root")
	s.Push(Frame{Door: "well8"})
	s.ViewPending = true
	var none Frame
	if none.SetView(4.5, 3.5, 0) {
		t.Fatal("SetView took a zoom of 0")
	}
	if s.Adopt(none) {
		t.Error("Adopt settled a pending frame on a frame with no view")
	}
	if !s.ViewPending {
		t.Error("the frame is no longer pending after a view that is not one")
	}
	var v Frame
	v.SetView(4.5, 3.5, 0.5)
	if !s.Adopt(v) || s.ViewPending || s.View != v.View {
		t.Errorf("Adopt of a view = pending %v view %+v, want %+v", s.ViewPending, s.View, v.View)
	}
}
