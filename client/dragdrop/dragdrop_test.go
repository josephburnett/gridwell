package dragdrop

import (
	"math"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestScreenToCellRoundTrip(t *testing.T) {
	p := Pane{
		ScreenX: 100, ScreenY: 50, ScreenW: 800, ScreenH: 600,
		Cx: 5, Cy: 7, Zoom: 1.5, CellPx: 64,
	}
	for _, c := range []struct{ x, y float64 }{
		{0, 0}, {-3, 4}, {12.5, -2.25}, {1e3, -1e3},
	} {
		sx, sy := p.CellToScreen(c.x, c.y)
		gx, gy := p.ScreenToCell(sx, sy)
		if !near(gx, c.x) || !near(gy, c.y) {
			t.Errorf("round trip (%v,%v) -> (%v,%v)", c.x, c.y, gx, gy)
		}
	}
}

func TestPaneAt(t *testing.T) {
	panes := []Pane{
		{ScreenX: 0, ScreenY: 0, ScreenW: 100, ScreenH: 100},
		{ScreenX: 100, ScreenY: 0, ScreenW: 200, ScreenH: 100},
		{ScreenX: 0, ScreenY: 100, ScreenW: 300, ScreenH: 200},
	}
	cases := []struct {
		sx, sy float64
		want   int
	}{
		{50, 50, 0},
		{150, 50, 1},
		{200, 200, 2},
		{1000, 1000, -1},
		{-5, 0, -1},
	}
	for _, c := range cases {
		if got := PaneAt(panes, c.sx, c.sy); got != c.want {
			t.Errorf("PaneAt(%v,%v) = %d, want %d", c.sx, c.sy, got, c.want)
		}
	}
}

func TestSnapToCell(t *testing.T) {
	cases := map[float64]int64{
		0: 0, 0.4: 0, 0.5: 1, 0.6: 1, 1.4: 1, 1.5: 2,
		-0.1: 0, -0.5: -1, -0.6: -1, -1.4: -1, -1.5: -2,
	}
	for in, want := range cases {
		if got := SnapToCell(in); got != want {
			t.Errorf("SnapToCell(%v) = %d, want %d", in, got, want)
		}
	}
}

func TestEdgeBand(t *testing.T) {
	// Big pane: capped at 80 px.
	big := Pane{ScreenW: 1920, ScreenH: 1080}
	if got := EdgeBand(big); got != 80 {
		t.Errorf("EdgeBand big = %v, want 80", got)
	}
	// Small pane: 12% of smaller dim.
	small := Pane{ScreenW: 200, ScreenH: 400}
	if got := EdgeBand(small); !near(got, 24) {
		t.Errorf("EdgeBand small = %v, want 24", got)
	}
	// Equal-sized: still 12% if under 666.
	mid := Pane{ScreenW: 500, ScreenH: 500}
	if got := EdgeBand(mid); !near(got, 60) {
		t.Errorf("EdgeBand mid = %v, want 60", got)
	}
}

func TestIsInEdgeZone(t *testing.T) {
	p := Pane{ScreenX: 0, ScreenY: 0, ScreenW: 100, ScreenH: 100}
	cases := []struct {
		x, y float64
		want bool
	}{
		{50, 50, false},  // dead center: not in edge.
		{2, 50, true},    // near left edge.
		{98, 50, true},   // near right edge.
		{50, 2, true},    // near top edge.
		{50, 98, true},   // near bottom edge.
		{20, 20, false},  // inside band.
		{0, 0, true},     // exact corner.
		{100, 100, true}, // bottom-right corner.
	}
	for _, c := range cases {
		if got := IsInEdgeZone(p, c.x, c.y, 10); got != c.want {
			t.Errorf("IsInEdgeZone(%v,%v) = %v, want %v", c.x, c.y, got, c.want)
		}
	}
}

func TestClosestEdge(t *testing.T) {
	p := Pane{ScreenX: 0, ScreenY: 0, ScreenW: 100, ScreenH: 100}
	cases := []struct {
		name string
		x, y float64
		want Side
	}{
		{"upper third center → top", 50, 10, SideTop},
		{"lower third center → bottom", 50, 90, SideBottom},
		{"left third center → left", 10, 50, SideLeft},
		{"right third center → right", 90, 50, SideRight},
		{"upper-left corner → top (tie favors top)", 5, 5, SideTop},
		{"upper-right corner → top (tie favors top)", 95, 5, SideTop},
		{"dead center → top (deterministic tiebreak)", 50, 50, SideTop},
		{"slightly above center → top", 50, 49, SideTop},
		{"slightly below center → bottom", 50, 51, SideBottom},
	}
	for _, c := range cases {
		if got := ClosestEdge(p, c.x, c.y); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestChildPreviewRoundTrip(t *testing.T) {
	parent := Pane{
		ScreenX: 0, ScreenY: 0, ScreenW: 800, ScreenH: 600,
		Cx: 0, Cy: 0, Zoom: 1.0, CellPx: 64,
	}
	well := struct {
		X, Y, W, H, ViewX, ViewY int64
	}{X: -1, Y: 2, W: 3, H: 4, ViewX: 10, ViewY: -5}
	// previewRatio = 1/8: legacy PreviewFactor fallback for an
	// unvisited well. previewCell = 64 × 1/8 = 8 px per child cell.
	cp := ChildPreviewFor(parent, well, 1.0/8.0)
	if !near(cp.CellPx, 8) {
		t.Errorf("CellPx = %v, want 8", cp.CellPx)
	}
	// Round-trip a few child cell coords through the screen mapping.
	for _, c := range []struct{ cx, cy float64 }{
		{0, 0}, {10, -5}, {11.5, -4.25}, {-7, 12},
	} {
		sx, sy := cp.CellToScreen(c.cx, c.cy)
		gx, gy := cp.ChildCellAtScreen(sx, sy)
		if !near(gx, c.cx) || !near(gy, c.cy) {
			t.Errorf("round trip (%v,%v) -> (%v,%v)", c.cx, c.cy, gx, gy)
		}
	}
}

func TestChildPreviewCenterAlignsWithViewCenter(t *testing.T) {
	// The preview's view center should land at the well's screen
	// center — that is the calibration zoomtrans relies on.
	parent := Pane{
		ScreenX: 0, ScreenY: 0, ScreenW: 1000, ScreenH: 1000,
		Cx: 0, Cy: 0, Zoom: 2.0, CellPx: 64,
	}
	well := struct {
		X, Y, W, H, ViewX, ViewY int64
	}{X: 0, Y: 0, W: 4, H: 4, ViewX: 0, ViewY: 0}
	cp := ChildPreviewFor(parent, well, 1.0/8.0)
	parentCell := parent.CellPx * parent.Zoom
	wellCenterX, wellCenterY := parent.CellToScreen(2, 2) // center of 4×4 well at (0,0)
	_ = parentCell
	// View center of the well is also (2, 2) in child-cell coords.
	viewCenterScreenX, viewCenterScreenY := cp.CellToScreen(2, 2)
	if !near(viewCenterScreenX, wellCenterX) || !near(viewCenterScreenY, wellCenterY) {
		t.Errorf("view center maps to (%v,%v), want (%v,%v)",
			viewCenterScreenX, viewCenterScreenY, wellCenterX, wellCenterY)
	}
}

func TestNodeContainsCell(t *testing.T) {
	cases := []struct {
		x, y, w, h, cx, cy int64
		want               bool
	}{
		{0, 0, 1, 1, 0, 0, true},
		{0, 0, 1, 1, 1, 0, false},
		{0, 0, 1, 1, 0, 1, false},
		{2, 3, 4, 5, 2, 3, true},
		{2, 3, 4, 5, 5, 7, true},
		{2, 3, 4, 5, 6, 7, false},
		{2, 3, 4, 5, 5, 8, false},
		{-2, -3, 2, 2, -2, -3, true},
		{-2, -3, 2, 2, -1, -2, true},
		{-2, -3, 2, 2, 0, -2, false},
	}
	for _, c := range cases {
		got := NodeContainsCell(c.x, c.y, c.w, c.h, c.cx, c.cy)
		if got != c.want {
			t.Errorf("NodeContainsCell(%d,%d,%d,%d,%d,%d) = %v, want %v",
				c.x, c.y, c.w, c.h, c.cx, c.cy, got, c.want)
		}
	}
}

func TestFootprintFits(t *testing.T) {
	p := Pane{
		ScreenX: 0, ScreenY: 0, ScreenW: 640, ScreenH: 640,
		Cx: 0, Cy: 0, Zoom: 1.0, CellPx: 64,
	}
	// 640 / 64 = 10 cells visible, centered on 0,0 -> [-5..5).
	if !p.FootprintFits(-2, -2, 1, 1) {
		t.Error("expected fit")
	}
	if !p.FootprintFits(-5, -5, 1, 1) {
		t.Error("expected flush-edge to fit")
	}
	if p.FootprintFits(5, 0, 1, 1) {
		t.Error("expected right-edge overflow")
	}
	if p.FootprintFits(-6, 0, 1, 1) {
		t.Error("expected left-edge overflow")
	}
}
