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

func TestChildPreviewRoundTrip(t *testing.T) {
	parent := Pane{
		ScreenX: 0, ScreenY: 0, ScreenW: 800, ScreenH: 600,
		Cx: 0, Cy: 0, Zoom: 1.0, CellPx: 64,
	}
	well := struct {
		X, Y, W, H     int64
		ViewCx, ViewCy float64
	}{X: -1, Y: 2, W: 3, H: 4, ViewCx: 11.5, ViewCy: -3}
	// An unvisited well falls back to a ratio of 1/8, so a 64 px parent
	// cell gives 8 px child cells.
	cp := ChildPreviewFor(parent, well, 1.0/8.0)
	if !near(cp.CellPx, 8) {
		t.Errorf("CellPx = %v, want 8", cp.CellPx)
	}
	// Round-trip child cell coordinates through the screen mapping.
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
	// The preview's view center lands at the well's screen center, which
	// is the calibration zoomtrans relies on.
	parent := Pane{
		ScreenX: 0, ScreenY: 0, ScreenW: 1000, ScreenH: 1000,
		Cx: 0, Cy: 0, Zoom: 2.0, CellPx: 64,
	}
	well := struct {
		X, Y, W, H     int64
		ViewCx, ViewCy float64
	}{X: 0, Y: 0, W: 4, H: 4, ViewCx: 2, ViewCy: 2}
	cp := ChildPreviewFor(parent, well, 1.0/8.0)
	parentCell := parent.CellPx * parent.Zoom
	wellCenterX, wellCenterY := parent.CellToScreen(2, 2) // center of 4×4 well at (0,0)
	_ = parentCell
	// The well's view center is also (2, 2) in child cells.
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

// SnapToCell would round the lower-right portion forward and miss half of
// every tile.
func TestFloorCellAtCoversWholeCell(t *testing.T) {
	const origin = 100.0
	const cs = 10.0
	// Every case wanting (0, 0) would round elsewhere under SnapToCell.
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
		// Catch anything that aliases the two functions.
		snapX := SnapToCell((c.sx - origin) / cs)
		snapY := SnapToCell((c.sy - origin) / cs)
		if c.sx == 105.0 && c.sy == 105.0 && snapX == gotX && snapY == gotY {
			t.Error("SnapToCell unexpectedly agrees at the 0.5 midpoint — guard test no longer protective")
		}
	}
}

// A match on anything a clone shares with its source would suppress every
// clone of the dragged tile.
func TestHiddenMatchByTileIDNotLineage(t *testing.T) {
	const sourceID = "5"
	const cloneID = "7" // a different row showing the same content
	const otherID = "9"

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
	if HiddenMatch("", "p1", "p1", sourceID) {
		t.Error(`no active hide (hiddenTileID=="") must hide nothing`)
	}
}

func TestInTileCenter(t *testing.T) {
	// A 3x3 tile at the origin has a center band of [1, 2] on both axes.
	cases := []struct {
		name         string
		x, y, w, h   int64
		cellX, cellY float64
		wantInCenter bool
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
		name         string
		pin, moving  int64
		origRight    bool
		wantS, wantL int64
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
	// A tile at (10, 20, 4, 4), clicked in the bottom-right quadrant, pins
	// the top-left corner.
	br := ResizeAnchorsFor(10, 20, 4, 4, 13.7, 23.7)
	if br.PinX != 10 || br.PinY != 20 || br.OrigMovingX != 14 || br.OrigMovingY != 24 {
		t.Errorf("BR quadrant: bad anchors %+v", br)
	}
	if br.ClickCellX != 14 || br.ClickCellY != 24 {
		t.Errorf("BR quadrant: bad click cell %+v", br)
	}
	// No cursor movement leaves the tile alone.
	x, y, w, h := ResizeFromCursor(br, br.ClickCellX, br.ClickCellY)
	if x != 10 || y != 20 || w != 4 || h != 4 {
		t.Errorf("BR + no movement: got (%d,%d,%d,%d), want (10,20,4,4)", x, y, w, h)
	}
	// Two cells right and one down grows the bottom-right corner.
	x, y, w, h = ResizeFromCursor(br, br.ClickCellX+2, br.ClickCellY+1)
	if x != 10 || y != 20 || w != 6 || h != 5 {
		t.Errorf("BR + (+2,+1): got (%d,%d,%d,%d), want (10,20,6,5)", x, y, w, h)
	}
	// Dragging back past the pin puts the cursor cell at PinX-1.
	x, y, w, h = ResizeFromCursor(br, br.PinX-1, br.PinY-1)
	if w < 1 || h < 1 {
		t.Errorf("crossover should keep w,h >= 1; got (%d,%d,%d,%d)", x, y, w, h)
	}

	// A click in the top-left quadrant pins the bottom-right corner.
	tl := ResizeAnchorsFor(10, 20, 4, 4, 10.2, 20.2)
	if tl.PinX != 14 || tl.PinY != 24 || tl.OrigMovingX != 10 || tl.OrigMovingY != 20 {
		t.Errorf("TL quadrant: bad anchors %+v", tl)
	}
	x, y, w, h = ResizeFromCursor(tl, tl.ClickCellX-1, tl.ClickCellY-1)
	if x != 9 || y != 19 || w != 5 || h != 5 {
		t.Errorf("TL + (-1,-1): got (%d,%d,%d,%d), want (9,19,5,5)", x, y, w, h)
	}
}

func TestPaneCellAt(t *testing.T) {
	// A 1000x800 pane centered on cell (0, 0), 64 px cells, zoom 1.
	p := Pane{
		ScreenX: 0, ScreenY: 0, ScreenW: 1000, ScreenH: 800,
		Cx: 0, Cy: 0, Zoom: 1, CellPx: 64,
	}
	// The pane center at (500, 400) is cell (0, 0).
	cx, cy := p.CellAt(500, 400)
	if cx != 0 || cy != 0 {
		t.Errorf("center: got (%d,%d), want (0,0)", cx, cy)
	}
	// One cell right of center is cell (1, 0).
	cx, cy = p.CellAt(500+64, 400)
	if cx != 1 || cy != 0 {
		t.Errorf("one cell right: got (%d,%d), want (1,0)", cx, cy)
	}
	// Just inside the next cell.
	cx, cy = p.CellAt(500+0.1, 400)
	if cx != 0 || cy != 0 {
		t.Errorf("just barely positive: got (%d,%d), want (0,0)", cx, cy)
	}
	// The lower-right half of cell (5, 3), where rounding would advance.
	sx, sy := p.CellToScreen(5.8, 3.8)
	cx, cy = p.CellAt(sx, sy)
	if cx != 5 || cy != 3 {
		t.Errorf("lower-right half: got (%d,%d), want (5,3)", cx, cy)
	}
}

// A click lands on what it hits: the verdict never asks which pane had focus,
// so there is no input that makes a bare click focus-only.
func TestDecideDropBareClickAlwaysNavigates(t *testing.T) {
	if got := DecideDrop(DropInput{Started: false, TileID: "u/1"}); got != DropNavigate {
		t.Errorf("bare click = %v, want DropNavigate", got)
	}
	if got := DecideDrop(DropInput{Started: false, SplitNav: true, TileID: "u/1"}); got != DropNavigateSplit {
		t.Errorf("ctrl bare click = %v, want DropNavigateSplit", got)
	}
}

// Rejected before the RPC, so no reconcile notice follows.
func TestDecideDropTargetReadOnly(t *testing.T) {
	base := DropInput{Started: true, TileID: "u/1", HasTarget: true, TargetReadOnly: true}
	if got := DecideDrop(base); got != DropRejected {
		t.Errorf("move onto read-only grid = %v, want DropRejected", got)
	}
	clone := base
	clone.Intent = IntentCopy
	if got := DecideDrop(clone); got != DropRejected {
		t.Errorf("clone onto read-only grid = %v, want DropRejected", got)
	}
	ok := base
	ok.TargetReadOnly = false
	if got := DecideDrop(ok); got != DropMove {
		t.Errorf("move onto writable grid = %v, want DropMove", got)
	}
}

// Read-only gates arrivals, not placement: a same-grid left-drag is a
// rearrangement the node persists on every grid.
func TestDecideDropReadOnlyPlacement(t *testing.T) {
	rearrange := DropInput{Started: true, TileID: "u/1", HasTarget: true,
		TargetReadOnly: true, SameGrid: true}
	if got := DecideDrop(rearrange); got != DropMove {
		t.Errorf("same-grid move on read-only grid = %v, want DropMove", got)
	}
	clone := rearrange
	clone.Intent = IntentCopy
	if got := DecideDrop(clone); got != DropRejected {
		t.Errorf("same-grid clone on read-only grid = %v, want DropRejected (creation)", got)
	}
}

// Host to host across grids is the case an XOR check would report allowed,
// inviting a drop the server then rejects.
func TestMoveForbidden(t *testing.T) {
	cases := []struct {
		name             string
		sameGrid         bool
		crossPlugin      bool
		srcHost, dstHost bool
		want             bool
	}{
		{"same grid, both host", true, false, true, true, false},
		{"same grid, regular", true, false, false, false, false},
		{"cross regular->regular", false, false, false, false, false},
		{"cross host->regular", false, false, true, false, true},
		{"cross regular->host", false, false, false, true, true},
		{"cross host->host (regression)", false, false, true, true, true},
		// A drag across an id namespace is a link, so the host arms are
		// exempt and TargetReadOnly gates the destination.
		{"cross-plugin left-drag is a link, not forbidden", false, true, false, false, false},
		{"cross-plugin from a host grid links too", false, true, true, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := MoveForbidden(c.sameGrid, c.crossPlugin, c.srcHost, c.dstHost); got != c.want {
				t.Errorf("MoveForbidden(%v, %v, %v, %v) = %v, want %v", c.sameGrid, c.crossPlugin, c.srcHost, c.dstHost, got, c.want)
			}
		})
	}
}

// Three call sites branch on this, so a wrong answer lets a copy land on its
// own source or gates a rearrangement a read-only grid should accept.
func TestIntentCreates(t *testing.T) {
	var zero Intent
	if zero != IntentMove {
		t.Errorf("zero Intent = %v, want IntentMove", zero)
	}
	for _, c := range []struct {
		in   Intent
		want bool
	}{
		{IntentMove, false},
		{IntentCopy, true},
		{IntentLink, true},
	} {
		if got := c.in.Creates(); got != c.want {
			t.Errorf("Intent(%d).Creates() = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestDecideDrop(t *testing.T) {
	// base is a started left-drag of a real tile over an empty target
	// cell, the DropMove case. Each row flips only the fields under test.
	base := DropInput{Started: true, TileID: "7", HasTarget: true}

	cases := []struct {
		name string
		in   DropInput
		want DropAction
	}{
		// The happy paths.
		{"clean left drag -> move", base, DropMove},
		{"clean right drag -> clone",
			DropInput{Started: true, TileID: "7", HasTarget: true, Intent: IntentCopy}, DropClone},

		// Early branches beat everything.
		{"bare click -> navigate (beats all)",
			DropInput{Started: false, IsTemplate: true, TileID: "7", OverDelete: true, HasTarget: true}, DropNavigate},
		{"ctrl bare click -> navigate in a split",
			DropInput{Started: false, SplitNav: true, TileID: "7"}, DropNavigateSplit},
		{"ctrl held on a started drag -> still a plain move",
			DropInput{Started: true, SplitNav: true, TileID: "7", HasTarget: true}, DropMove},
		{"template -> create (beats pan)",
			DropInput{Started: true, IsTemplate: true, TileID: "", HasTarget: true}, DropCreateTemplate},
		{"pan (tileID \"\") -> panEnd (beats delete and the target check)",
			DropInput{Started: true, TileID: "", OverDelete: true, HasTarget: true}, DropPanEnd},
		{"pan off any pane -> still panEnd",
			DropInput{Started: true, TileID: "", HasTarget: false}, DropPanEnd},
		// A creation needs a destination like every other landing arm. A
		// swatch released over a content descent resolves no target.
		{"template with no target -> rejected",
			DropInput{Started: true, IsTemplate: true, TileID: "", HasTarget: false}, DropRejected},

		// Delete fires and outranks the placement arms.
		{"over delete button -> delete",
			DropInput{Started: true, TileID: "7", OverDelete: true}, DropDelete},
		{"delete wins over an occupied target (precedence)",
			DropInput{Started: true, TileID: "7", OverDelete: true, HasTarget: true, Occupied: true}, DropDelete},
		{"delete fires even with no target",
			DropInput{Started: true, TileID: "7", OverDelete: true, HasTarget: false}, DropDelete},

		// Rejection cases, one cause each.
		{"no target -> rejected",
			DropInput{Started: true, TileID: "7", HasTarget: false}, DropRejected},
		{"forbidden cross-grid move -> rejected",
			DropInput{Started: true, TileID: "7", HasTarget: true, Forbidden: true}, DropRejected},
		{"same cell -> rejected",
			DropInput{Started: true, TileID: "7", HasTarget: true, SameCell: true}, DropRejected},
		{"occupied -> rejected",
			DropInput{Started: true, TileID: "7", HasTarget: true, Occupied: true}, DropRejected},

		// The copy intent.
		{"clone with a clean target -> clone",
			DropInput{Started: true, TileID: "7", HasTarget: true, Intent: IntentCopy}, DropClone},
		// SameCell and Occupied still reject a clone.
		{"clone onto occupied -> rejected",
			DropInput{Started: true, TileID: "7", HasTarget: true, Intent: IntentCopy, Occupied: true}, DropRejected},
		{"clone onto same cell -> rejected",
			DropInput{Started: true, TileID: "7", HasTarget: true, Intent: IntentCopy, SameCell: true}, DropRejected},
		// Forbidden is a per-gesture input, and the verdict treats a
		// forbidden clone like a forbidden move.
		{"forbidden clone -> rejected",
			DropInput{Started: true, TileID: "7", HasTarget: true, Intent: IntentCopy, Forbidden: true}, DropRejected},

		// The link intent, ctrl with a right-drag. The gesture asks for a
		// link, so it links inside one namespace too.
		{"ctrl right drag in one namespace -> link",
			DropInput{Started: true, TileID: "7", HasTarget: true, Intent: IntentLink}, DropLink},
		// Crossing a namespace with the modifier held reaches the same
		// verdict a plain left-drag does.
		{"ctrl right drag across a namespace -> link",
			DropInput{Started: true, TileID: "7", HasTarget: true, Intent: IntentLink, CrossPlugin: true}, DropLink},
		// A link lands a new row, so the source cell is a real neighbor
		// and a read-only grid refuses it.
		{"ctrl right drag onto occupied -> rejected",
			DropInput{Started: true, TileID: "7", HasTarget: true, Intent: IntentLink, Occupied: true}, DropRejected},
		{"ctrl right drag onto same cell -> rejected",
			DropInput{Started: true, TileID: "7", HasTarget: true, Intent: IntentLink, SameCell: true}, DropRejected},
		{"ctrl right drag onto a read-only grid -> rejected",
			DropInput{Started: true, TileID: "7", HasTarget: true, Intent: IntentLink, TargetReadOnly: true}, DropRejected},
		{"ctrl right drag in the tile's own read-only grid -> rejected (creation)",
			DropInput{Started: true, TileID: "7", HasTarget: true, Intent: IntentLink, TargetReadOnly: true, SameGrid: true}, DropRejected},
		{"ctrl right drag over delete still deletes",
			DropInput{Started: true, TileID: "7", OverDelete: true, Intent: IntentLink}, DropDelete},

		// A cross-namespace left-drag is a link.
		{"cross-plugin left drag -> link",
			DropInput{Started: true, TileID: "7", HasTarget: true, CrossPlugin: true}, DropLink},
		{"cross-plugin right drag -> clone (copy, not link)",
			DropInput{Started: true, TileID: "7", HasTarget: true, CrossPlugin: true, Intent: IntentCopy}, DropClone},
		{"cross-plugin link onto occupied -> rejected",
			DropInput{Started: true, TileID: "7", HasTarget: true, CrossPlugin: true, Occupied: true}, DropRejected},
		{"cross-plugin link onto read-only target -> rejected",
			DropInput{Started: true, TileID: "7", HasTarget: true, CrossPlugin: true, TargetReadOnly: true}, DropRejected},
		{"cross-plugin drop over delete still deletes",
			DropInput{Started: true, TileID: "7", OverDelete: true, CrossPlugin: true}, DropDelete},
	}
	for _, c := range cases {
		if got := DecideDrop(c.in); got != c.want {
			t.Errorf("%s: DecideDrop(%+v) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}

func TestGhostPlanForDrop(t *testing.T) {
	const (
		origin = "origin"
		target = "target"
		doc    = "doc"
		srcSz  = 50.0
		tgtSz  = 80.0
	)
	cases := []struct {
		name      string
		action    DropAction
		forbidden bool
		want      GhostPlan
	}{
		{"delete shrinks+fragments in origin", DropDelete, false,
			GhostPlan{PaneID: origin, TargetCellSize: srcSz * 0.2, Fragmentation: 1.0}},
		{"rejected forbidden cross-grid: no-entry in target", DropRejected, true,
			GhostPlan{PaneID: target, TargetCellSize: srcSz, Forbidden: true, Cursor: "not-allowed"}},
		{"rejected off-canvas: glide back in origin, no badge", DropRejected, false,
			GhostPlan{PaneID: origin, TargetCellSize: srcSz}},
		{"move snaps to target cell", DropMove, false,
			GhostPlan{PaneID: target, TargetCellSize: tgtSz}},
		{"clone snaps to target cell", DropClone, false,
			GhostPlan{PaneID: target, TargetCellSize: tgtSz}},
		// A cross-namespace left-drag previews as a link, or the source's
		// survival after the drop would read as a surprise duplicate.
		{"link snaps to target cell with the chain badge", DropLink, false,
			GhostPlan{PaneID: target, TargetCellSize: tgtSz, Link: true}},
	}
	for _, c := range cases {
		got := GhostPlanForDrop(c.action, c.forbidden,
			origin, target, srcSz, tgtSz)
		if got != c.want {
			t.Errorf("%s: GhostPlanForDrop = %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestPromoteToWell(t *testing.T) {
	cases := []struct {
		name                               string
		isWell                             bool
		childGridID, tileID, draggedTileID string
		want                               bool
	}{
		{"open well promotes", true, "g9", "t1", "t2", true},
		{"non-well never promotes", false, "g9", "t1", "t2", false},
		{"well with no child never promotes", true, "", "t1", "t2", false},
		{"the dragged well itself never promotes (self-cycle)", true, "g9", "t1", "t1", false},
		{"no drag in flight still promotes", true, "g9", "t1", "", true},
	}
	for _, c := range cases {
		if got := PromoteToWell(c.isWell, c.childGridID, c.tileID, c.draggedTileID); got != c.want {
			t.Errorf("%s: PromoteToWell = %v, want %v", c.name, got, c.want)
		}
	}
}

// RectsOverlap is the client half of the placement collision contract:
// strict interior intersection, so edge adjacency is not a collision.
func TestRectsOverlap(t *testing.T) {
	cases := []struct {
		name           string
		ax, ay, aw, ah int64
		bx, by, bw, bh int64
		want           bool
	}{
		{"identical", 0, 0, 2, 2, 0, 0, 2, 2, true},
		{"one-cell shift of a 2x2 (the #231 self-cross)", 0, 0, 2, 2, 1, 0, 2, 2, true},
		{"corner touch only", 0, 0, 2, 2, 2, 2, 2, 2, false},
		{"edge adjacent", 0, 0, 2, 2, 2, 0, 2, 2, false},
		{"contained", 0, 0, 4, 4, 1, 1, 1, 1, true},
		{"disjoint", 0, 0, 2, 2, 5, 5, 1, 1, false},
	}
	for _, c := range cases {
		if got := RectsOverlap(c.ax, c.ay, c.aw, c.ah, c.bx, c.by, c.bw, c.bh); got != c.want {
			t.Errorf("%s: RectsOverlap = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPaneShows(t *testing.T) {
	p := Pane{ScreenW: 640, ScreenH: 640, Zoom: 1, CellPx: 64}
	cases := []struct {
		name       string
		x, y, w, h float64
		want       bool
	}{
		{"at the center", 0, 0, 1, 1, true},
		{"straddling the left edge", -6, 0, 2, 1, true},
		{"touching the left edge", -6, 0, 1, 1, true},
		{"past the left edge", -8, 0, 1, 1, false},
		{"past the bottom edge", 0, 6, 1, 1, false},
	}
	for _, c := range cases {
		if got := p.Shows(c.x, c.y, c.w, c.h); got != c.want {
			t.Errorf("%s: Shows = %v, want %v", c.name, got, c.want)
		}
	}
}
