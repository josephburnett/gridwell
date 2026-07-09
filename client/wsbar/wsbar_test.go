package wsbar

import "testing"

func TestHeightOnlyInsideAWorkspace(t *testing.T) {
	if Height(0) != 0 {
		t.Fatal("the session tree / landing page must have no bar")
	}
	if Height(1) != RowH || Height(3) != RowH {
		t.Fatal("one band regardless of depth (breadcrumbs, not stacked bars)")
	}
}

// TestSegmentsAndHitTestAgree: render and input read the same rects, so the
// crumb drawn at a point is the crumb a click there resolves to.
func TestSegmentsAndHitTestAgree(t *testing.T) {
	segs := Segments(3, 900)
	if len(segs) != 3 {
		t.Fatalf("segments = %d, want 3", len(segs))
	}
	for _, s := range segs {
		if got := SegmentAt(segs, s.X+s.W/2); got != s.Level {
			t.Errorf("center of crumb %d hit-tests as %d", s.Level, got)
		}
	}
	if SegmentAt(segs, segs[2].X+segs[2].W+50) != 0 {
		t.Error("beyond the last crumb must hit nothing")
	}
	if SegmentAt(nil, 10) != 0 {
		t.Error("empty bar must hit nothing")
	}
}

func TestSegmentsCapWidth(t *testing.T) {
	segs := Segments(1, 2000)
	if segs[0].W != maxCrumbW {
		t.Fatalf("single crumb w = %v, want capped %v", segs[0].W, maxCrumbW)
	}
	segs = Segments(10, 1000)
	if segs[0].W != 100 {
		t.Fatalf("deep nesting divides evenly: w = %v, want 100", segs[0].W)
	}
}
