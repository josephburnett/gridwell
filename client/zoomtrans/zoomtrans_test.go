package zoomtrans

import (
	"math"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

const cellPx = 64.0

func TestOvertakeZoomTakesLargerOfDimRatios(t *testing.T) {
	// 1x1 well, 1920x1080 pane → must zoom 1920/64=30 to overtake width.
	z := OvertakeZoom(Well{W: 1, H: 1}, 1920, 1080, cellPx)
	if !near(z, 30) {
		t.Errorf("z = %v, want 30", z)
	}
	// 1x1 well, 1080x1920 pane → must zoom 1920/64 to overtake height.
	z = OvertakeZoom(Well{W: 1, H: 1}, 1080, 1920, cellPx)
	if !near(z, 30) {
		t.Errorf("portrait z = %v, want 30", z)
	}
	// 3x2 well, 1920x1080 pane → max(1920/192, 1080/128) = max(10, 8.4375).
	z = OvertakeZoom(Well{W: 3, H: 2}, 1920, 1080, cellPx)
	if !near(z, 10) {
		t.Errorf("z = %v, want 10", z)
	}
}

func TestOvertakeZoomGuards(t *testing.T) {
	if OvertakeZoom(Well{W: 0, H: 1}, 100, 100, cellPx) != 1 {
		t.Error("zero w should return 1")
	}
	if OvertakeZoom(Well{W: 1, H: 1}, 100, 100, 0) != 1 {
		t.Error("zero cellPx should return 1")
	}
}

func TestDescentMidIsOvertakeAndContinuity(t *testing.T) {
	from := Endpoints{Path: nil, Cx: 0, Cy: 0, Zoom: 1.0}
	w := Well{ID: 7, X: 5, Y: 3, W: 1, H: 1, ViewX: 0, ViewY: 0}
	mid, to := Descent(from, w, 1920, 1080, cellPx)

	// Mid centers on well center; zoom is the overtake zoom (30 for the
	// 1x1 / 1920 case).
	if !near(mid.Cx, 5.5) || !near(mid.Cy, 3.5) {
		t.Errorf("mid center = (%v, %v)", mid.Cx, mid.Cy)
	}
	if !near(mid.Zoom, 30) {
		t.Errorf("mid zoom = %v, want 30", mid.Zoom)
	}
	// To has new path with well appended; viewport on well's view region;
	// zoom = mid.Zoom / PreviewFactor (calibration check).
	if len(to.Path) != 1 || to.Path[0] != 7 {
		t.Errorf("to path = %v", to.Path)
	}
	if !near(to.Cx, 0.5) || !near(to.Cy, 0.5) {
		t.Errorf("to center = (%v, %v)", to.Cx, to.Cy)
	}
	if !near(to.Zoom, 30/PreviewFactor) {
		t.Errorf("to zoom = %v, want %v", to.Zoom, 30/PreviewFactor)
	}
}

func TestDescentNeverZoomsOut(t *testing.T) {
	// Caller already zoomed in past the overtake zoom: descent must not
	// regress. mid.Zoom should be at least from.Zoom.
	from := Endpoints{Zoom: 50}
	w := Well{W: 1, H: 1}
	mid, _ := Descent(from, w, 1920, 1080, cellPx)
	if mid.Zoom < from.Zoom {
		t.Errorf("mid.Zoom = %v, want >= %v", mid.Zoom, from.Zoom)
	}
}

func TestDescentDoesNotShareSlice(t *testing.T) {
	from := Endpoints{Path: []int64{1, 2, 3}, Zoom: 1}
	w := Well{ID: 9}
	_, to := Descent(from, w, 100, 100, cellPx)
	to.Path[0] = 999
	if from.Path[0] == 999 {
		t.Error("Descent shared the path slice")
	}
}

func TestAscentNeverZoomsIn(t *testing.T) {
	// Caller is already at a tiny zoom; ascent must not zoom in.
	from := Endpoints{Path: []int64{42}, Zoom: 0.5}
	w := Well{ID: 42, W: 1, H: 1, ViewX: 1, ViewY: 1}
	mid, _ := Ascent(from, w, nil, 1920, 1080, cellPx)
	if mid.Zoom > from.Zoom {
		t.Errorf("mid.Zoom = %v, want <= %v", mid.Zoom, from.Zoom)
	}
}

func TestAscentSwitchContinuity(t *testing.T) {
	// At the switch: child cell = cellPx * mid.Zoom; preview cell =
	// cellPx * to.Zoom / PreviewFactor. Equal => to.Zoom = mid.Zoom *
	// PreviewFactor.
	from := Endpoints{Path: []int64{42}, Zoom: 5.0}
	w := Well{ID: 42, X: 1, Y: 2, W: 2, H: 1, ViewX: 0, ViewY: 0}
	mid, to := Ascent(from, w, nil, 1920, 1080, cellPx)
	if !near(to.Zoom, mid.Zoom*PreviewFactor) {
		t.Errorf("to.Zoom = %v, mid.Zoom*PreviewFactor = %v", to.Zoom, mid.Zoom*PreviewFactor)
	}
	// And the parent's viewport is centered on the well rect's center.
	if !near(to.Cx, 2) || !near(to.Cy, 2.5) {
		t.Errorf("to center = (%v, %v); want (2, 2.5)", to.Cx, to.Cy)
	}
}
