package wsbar

import "testing"

// Render and input read the same rects, so the crumb drawn at a point is
// the crumb a click there resolves to.
func TestLayoutAndHitTestAgree(t *testing.T) {
	segs := Layout(7, 900)
	if len(segs) != 7 {
		t.Fatalf("segments = %d, want all 7 (room for them)", len(segs))
	}
	for _, s := range segs {
		got, ok := At(segs, s.X+s.W/2)
		if !ok || got.Index != s.Index {
			t.Errorf("center of segment %+v hit-tests as %+v (ok=%v)", s, got, ok)
		}
	}
	last := segs[len(segs)-1]
	if _, ok := At(segs, last.X+last.W+50); ok {
		t.Error("beyond the last crumb must hit nothing")
	}
	if _, ok := At(nil, 10); ok {
		t.Error("empty bar must hit nothing")
	}
}

// Every crumb is a full RowH square, abutting its neighbor, starting at
// x=0 — one uniform chain (issue #245).
func TestLayoutSquares(t *testing.T) {
	segs := Layout(3, 1000)
	if segs[0].X != 0 || segs[0].Index != 0 {
		t.Fatalf("segment 0 = %+v, want index 0 at x=0", segs[0])
	}
	for i, s := range segs {
		if s.W != RowH {
			t.Fatalf("crumb w = %v, want the full square %v (no shrinking)", s.W, RowH)
		}
		if i > 0 && s.X != segs[i-1].X+segs[i-1].W {
			t.Fatalf("segment %d does not abut its neighbor", i)
		}
	}
}

// Overflow truncates from the LEFT (issue #245): the tail — where you are
// — keeps priority, survivors keep full size, and Index keeps addressing
// the caller's full crumb list.
func TestLayoutTruncatesFromLeft(t *testing.T) {
	width := SlotW + 3*RowH + 10 // room for exactly 3 squares
	segs := Layout(10, width)
	if len(segs) != 3 {
		t.Fatalf("visible = %d, want 3", len(segs))
	}
	if segs[0].Index != 7 || segs[2].Index != 9 {
		t.Fatalf("visible indexes = [%d..%d], want the tail [7..9]", segs[0].Index, segs[2].Index)
	}
	if segs[0].X != 0 {
		t.Fatalf("first visible crumb starts at %v, want 0", segs[0].X)
	}
	// A truncated-away crumb has no rect; SegmentAt says so.
	if _, ok := SegmentAt(segs, 2); ok {
		t.Error("truncated crumb must not resolve")
	}
	if s, ok := SegmentAt(segs, 9); !ok || s.Index != 9 {
		t.Errorf("tail crumb must resolve: %+v ok=%v", s, ok)
	}
}

func TestLayoutDegenerate(t *testing.T) {
	if segs := Layout(0, 900); segs != nil {
		t.Errorf("no crumbs: %v, want nil", segs)
	}
	if segs := Layout(5, SlotW+10); segs != nil {
		t.Errorf("no room for even one square: %v, want nil", segs)
	}
}
