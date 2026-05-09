package zoomtrans

import (
	"math"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestDescentMidPointEnds(t *testing.T) {
	from := Endpoints{Path: nil, Cx: 0, Cy: 0, Zoom: 1.0}
	w := Well{ID: 7, X: 5, Y: 3, W: 1, H: 1, ViewX: 0, ViewY: 0}
	mid, to := Descent(from, w)

	// Mid centers on well center; zoom multiplied by PreviewFactor.
	if !near(mid.Cx, 5.5) || !near(mid.Cy, 3.5) {
		t.Errorf("mid center = (%v, %v)", mid.Cx, mid.Cy)
	}
	if !near(mid.Zoom, PreviewFactor) {
		t.Errorf("mid zoom = %v, want %v", mid.Zoom, PreviewFactor)
	}
	if len(mid.Path) != 0 {
		t.Errorf("mid path should still be parent path: %v", mid.Path)
	}

	// To has new path with well appended; viewport on well's view region;
	// zoom equal to original parent zoom (calibration check).
	if len(to.Path) != 1 || to.Path[0] != 7 {
		t.Errorf("to path = %v", to.Path)
	}
	if !near(to.Cx, 0.5) || !near(to.Cy, 0.5) {
		t.Errorf("to center = (%v, %v)", to.Cx, to.Cy)
	}
	if !near(to.Zoom, 1.0) {
		t.Errorf("to zoom = %v, want 1.0", to.Zoom)
	}
}

func TestDescentDoesNotShareSlice(t *testing.T) {
	from := Endpoints{Path: []int64{1, 2, 3}, Zoom: 1}
	w := Well{ID: 9}
	_, to := Descent(from, w)
	// Mutating returned path must not affect caller's path.
	to.Path[0] = 999
	if from.Path[0] == 999 {
		t.Error("Descent shared the path slice")
	}
}

func TestAscentSymmetryWithDescent(t *testing.T) {
	// Descend then ascend should round-trip the (parent) state.
	parent := Endpoints{Path: []int64{}, Cx: 4, Cy: 7, Zoom: 1.5}
	w := Well{ID: 42, X: 3, Y: 6, W: 2, H: 2, ViewX: 0, ViewY: 0}

	_, child := Descent(parent, w)
	// Now ascend from child back to parent.
	_, back := Ascent(child, w, parent.Path)

	// The ascent target lands on the well's center in the parent grid at
	// the same zoom we descended at. (We don't try to restore the parent's
	// prior viewport — that's a richer feature; symmetry of zoom-and-cell
	// is what matters.)
	if !near(back.Cx, 4) || !near(back.Cy, 7) {
		t.Errorf("ascent back center = (%v, %v); want well center (4,7)", back.Cx, back.Cy)
	}
	if !near(back.Zoom, parent.Zoom) {
		t.Errorf("ascent back zoom = %v, want %v", back.Zoom, parent.Zoom)
	}
	if len(back.Path) != 0 {
		t.Errorf("ascent back path = %v", back.Path)
	}
}

func TestAscentMidIsZoomedOut(t *testing.T) {
	from := Endpoints{Path: []int64{42}, Cx: 0, Cy: 0, Zoom: 2.0}
	w := Well{ID: 42, X: 5, Y: 5, W: 1, H: 1, ViewX: 1, ViewY: 1}

	mid, _ := Ascent(from, w, nil)
	// Mid Zoom should be from.Zoom / PreviewFactor.
	if !near(mid.Zoom, 2.0/PreviewFactor) {
		t.Errorf("mid zoom = %v, want %v", mid.Zoom, 2.0/PreviewFactor)
	}
	// Center on well's view region.
	if !near(mid.Cx, 1.5) || !near(mid.Cy, 1.5) {
		t.Errorf("mid center = (%v, %v)", mid.Cx, mid.Cy)
	}
}
