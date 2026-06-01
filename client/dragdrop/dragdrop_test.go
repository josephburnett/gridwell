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

func TestTileContainsCell(t *testing.T) {
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
		got := TileContainsCell(c.x, c.y, c.w, c.h, c.cx, c.cy)
		if got != c.want {
			t.Errorf("TileContainsCell(%d,%d,%d,%d,%d,%d) = %v, want %v",
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

// TestFloorCellAtCoversWholeCell guards against the "right-half misses"
// bug: a hit-test using SnapToCell on the same coords would round the
// lower-right portion of each cell forward to the NEXT cell, making
// any tile-under-cursor detection miss half its target.
//
// Concretely: when the cursor sat in the right or bottom half of a
// black-hole tile's 1×1 cell, the drop-on-blackhole detection (which
// originally used SnapToCell) failed to fire, so the tile didn't get
// deleted. FloorCellAt is the correct answer for "which cell am I in".
func TestFloorCellAtCoversWholeCell(t *testing.T) {
	const origin = 100.0
	const cs = 10.0
	// (sx, sy) → (wantX, wantY). All "wantX=0, wantY=0" cases below
	// would have rounded to (1, 1) or (0, 1) etc. under SnapToCell.
	cases := []struct {
		sx, sy float64
		wantX  int64
		wantY  int64
	}{
		{100.0, 100.0, 0, 0},   // top-left corner
		{100.5, 100.0, 0, 0},   // just inside left edge
		{104.5, 104.5, 0, 0},   // dead center
		{105.0, 105.0, 0, 0},   // SnapToCell would say (1,1) here
		{107.0, 107.0, 0, 0},   // right-half (SnapToCell: (1,1))
		{109.99, 109.99, 0, 0}, // bottom-right interior
		{110.0, 100.0, 1, 0},   // exact next-cell boundary on X
		{100.0, 110.0, 0, 1},   // exact next-cell boundary on Y
		{99.99, 100.0, -1, 0},  // just outside left → previous cell
		{100.0, 99.99, 0, -1},  // just outside top → previous cell
	}
	for _, c := range cases {
		gotX, gotY := FloorCellAt(origin, origin, cs, c.sx, c.sy)
		if gotX != c.wantX || gotY != c.wantY {
			t.Errorf("FloorCellAt(%.2f, %.2f) = (%d, %d), want (%d, %d)",
				c.sx, c.sy, gotX, gotY, c.wantX, c.wantY)
		}
		// Sanity: if SnapToCell agreed with FloorCellAt everywhere the
		// bug couldn't exist. Catch any regression that aliased the two.
		snapX := SnapToCell((c.sx - origin) / cs)
		snapY := SnapToCell((c.sy - origin) / cs)
		if c.sx == 105.0 && c.sy == 105.0 && snapX == gotX && snapY == gotY {
			t.Error("SnapToCell unexpectedly agrees at the 0.5 midpoint — guard test no longer protective")
		}
	}
}

// TestHiddenMatchByTileIDNotObjectID guards against the "cloned tile
// disappears when its sibling is picked up" bug. The render path used
// to compare tiles by ObjectID, but CloneTile deliberately copies the
// source's ObjectID — so a hide-by-ObjectID predicate suppresses every
// clone of the dragged tile, not just the dragged tile itself.
func TestHiddenMatchByTileIDNotObjectID(t *testing.T) {
	const sourceID int64 = 5
	const cloneID int64 = 7 // different row, same ObjectID upstream
	const otherID int64 = 9

	if !HiddenMatch(sourceID, "p1", "p1", sourceID) {
		t.Error("dragged source tile should be hidden in its pane")
	}
	if HiddenMatch(sourceID, "p1", "p1", cloneID) {
		t.Error("a clone (different row id) must NOT be hidden")
	}
	if HiddenMatch(sourceID, "p1", "p1", otherID) {
		t.Error("an unrelated tile must NOT be hidden")
	}
	if HiddenMatch(sourceID, "p1", "p2", sourceID) {
		t.Error("source tile in a DIFFERENT pane must NOT be hidden")
	}
	if HiddenMatch(0, "p1", "p1", sourceID) {
		t.Error("no active hide (hiddenTileID==0) must hide nothing")
	}
}

func TestInTileCenter(t *testing.T) {
	// 3x3 tile at origin: center band is cell coords [1, 2] on both axes.
	cases := []struct {
		name           string
		x, y, w, h     int64
		cellX, cellY   float64
		wantInCenter   bool
	}{
		{"dead center", 0, 0, 3, 3, 1.5, 1.5, true},
		{"on left edge of center band", 0, 0, 3, 3, 1.0, 1.5, true},
		{"on right edge of center band", 0, 0, 3, 3, 2.0, 1.5, true},
		{"just outside left", 0, 0, 3, 3, 0.99, 1.5, false},
		{"just outside top", 0, 0, 3, 3, 1.5, 0.99, false},
		{"1x1 tile, exact center", 5, 5, 1, 1, 5.5, 5.5, true},
		{"1x1 tile, edge", 5, 5, 1, 1, 5.0, 5.0, false},
		{"offset tile", 10, 20, 6, 6, 13, 23, true},
	}
	for _, c := range cases {
		got := InTileCenter(c.x, c.y, c.w, c.h, c.cellX, c.cellY)
		if got != c.wantInCenter {
			t.Errorf("%s: InTileCenter = %v, want %v", c.name, got, c.wantInCenter)
		}
	}
}

func TestRangeFromAnchors(t *testing.T) {
	cases := []struct {
		name          string
		pin, moving   int64
		origRight     bool
		wantS, wantL  int64
	}{
		{"moving > pin", 3, 8, true, 3, 5},
		{"moving < pin", 8, 3, true, 3, 5},
		{"degenerate, orig was right of pin", 5, 5, true, 5, 1},
		{"degenerate, orig was left of pin", 5, 5, false, 4, 1},
		{"negative pin", -2, 3, true, -2, 5},
	}
	for _, c := range cases {
		s, l := RangeFromAnchors(c.pin, c.moving, c.origRight)
		if s != c.wantS || l != c.wantL {
			t.Errorf("%s: RangeFromAnchors(%d,%d,%v) = (%d,%d), want (%d,%d)",
				c.name, c.pin, c.moving, c.origRight, s, l, c.wantS, c.wantL)
		}
	}
}

func TestResizeAnchorsAndCursor(t *testing.T) {
	// Original tile: (10, 20, 4, 4). Click in BR quadrant -> pin TL.
	br := ResizeAnchorsFor(10, 20, 4, 4, 13.7, 23.7)
	if br.PinX != 10 || br.PinY != 20 || br.OrigMovingX != 14 || br.OrigMovingY != 24 {
		t.Errorf("BR quadrant: bad anchors %+v", br)
	}
	if br.ClickCellX != 14 || br.ClickCellY != 24 {
		t.Errorf("BR quadrant: bad click cell %+v", br)
	}
	// No cursor movement => same tile.
	x, y, w, h := ResizeFromCursor(br, br.ClickCellX, br.ClickCellY)
	if x != 10 || y != 20 || w != 4 || h != 4 {
		t.Errorf("BR + no movement: got (%d,%d,%d,%d), want (10,20,4,4)", x, y, w, h)
	}
	// Drag cursor 2 cells right + 1 down => grow tile by (2, 1) on the BR.
	x, y, w, h = ResizeFromCursor(br, br.ClickCellX+2, br.ClickCellY+1)
	if x != 10 || y != 20 || w != 6 || h != 5 {
		t.Errorf("BR + (+2,+1): got (%d,%d,%d,%d), want (10,20,6,5)", x, y, w, h)
	}
	// Crossover: drag back past the pin so the cursor cell is at PinX-1.
	x, y, w, h = ResizeFromCursor(br, br.PinX-1, br.PinY-1)
	if w < 1 || h < 1 {
		t.Errorf("crossover should keep w,h >= 1; got (%d,%d,%d,%d)", x, y, w, h)
	}

	// Click in TL quadrant -> pin BR.
	tl := ResizeAnchorsFor(10, 20, 4, 4, 10.2, 20.2)
	if tl.PinX != 14 || tl.PinY != 24 || tl.OrigMovingX != 10 || tl.OrigMovingY != 20 {
		t.Errorf("TL quadrant: bad anchors %+v", tl)
	}
	x, y, w, h = ResizeFromCursor(tl, tl.ClickCellX-1, tl.ClickCellY-1)
	if x != 9 || y != 19 || w != 5 || h != 5 {
		t.Errorf("TL + (-1,-1): got (%d,%d,%d,%d), want (9,19,5,5)", x, y, w, h)
	}
}

func TestNearPx(t *testing.T) {
	if !NearPx(10.0, 10.4) {
		t.Error("0.4 px apart should be near")
	}
	if NearPx(10.0, 10.6) {
		t.Error("0.6 px apart should not be near")
	}
	if !NearPx(10.0, 9.51) {
		t.Error("0.49 px apart should be near")
	}
	if !NearPx(0, 0) {
		t.Error("identical should be near")
	}
}
