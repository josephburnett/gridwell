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

// TestFloorCellAtCoversWholeCell guards against the "right-half misses"
// bug: a hit-test using SnapToCell on the same coords would round the
// lower-right portion of each cell forward to the NEXT cell, making
// any tile-under-cursor detection miss half its target.
//
// Concretely: when the cursor sat in the right or bottom half of a
// 1×1 tile's cell, tile-under-cursor detection (which originally used
// SnapToCell) failed to fire over half the target. FloorCellAt is the
// correct answer for "which cell am I in".
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
	const sourceID = "5"
	const cloneID = "7" // different row, same ObjectID upstream
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
	// 3x3 tile at origin: center band is cell coords [1, 2] on both axes.
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

func TestPaneCellAt(t *testing.T) {
	// 1000x800 pane centered on cell (0,0), 64 px cells, zoom 1.
	p := Pane{
		ScreenX: 0, ScreenY: 0, ScreenW: 1000, ScreenH: 800,
		Cx: 0, Cy: 0, Zoom: 1, CellPx: 64,
	}
	// Pane center -> cell (0,0). Pane center is at (500, 400).
	cx, cy := p.CellAt(500, 400)
	if cx != 0 || cy != 0 {
		t.Errorf("center: got (%d,%d), want (0,0)", cx, cy)
	}
	// One cell right (+64 px) of center -> cell (1, 0).
	cx, cy = p.CellAt(500+64, 400)
	if cx != 1 || cy != 0 {
		t.Errorf("one cell right: got (%d,%d), want (1,0)", cx, cy)
	}
	// Just barely into next cell.
	cx, cy = p.CellAt(500+0.1, 400)
	if cx != 0 || cy != 0 {
		t.Errorf("just barely positive: got (%d,%d), want (0,0)", cx, cy)
	}
	// Lower-right half of cell (5,3) — floor wins, round would have advanced.
	sx, sy := p.CellToScreen(5.8, 3.8)
	cx, cy = p.CellAt(sx, sy)
	if cx != 5 || cy != 3 {
		t.Errorf("lower-right half: got (%d,%d), want (5,3)", cx, cy)
	}
}

// TestMoveForbidden pins the move-drop policy to the server's MoveTile rule.
// The regression is the source->source cross-grid case (e.g. dragging a file
// from one host directory's grid into another's): the server rejects any
// cross-grid move touching a source-backed grid, but the UI's old XOR check
// reported it allowed, inviting a drop that then failed.
// TestDecideDropFocusOnly: a bare click on a pane that was NOT focused at
// press time is focus-only — no navigation, no selection — no matter what
// sits under the cursor. A bare click on an already-focused pane navigates.
// Same family as the +-button rule (act only when previously focused);
// closes the "clicking a pane to focus it descends into a tile" ambiguity
// (issue #28).
func TestDecideDropFocusOnly(t *testing.T) {
	unfocused := DropInput{Started: false, OriginFocused: false, TileID: "u/1"}
	if got := DecideDrop(unfocused); got != DropFocusOnly {
		t.Errorf("bare click on unfocused pane = %v, want DropFocusOnly", got)
	}
	focused := DropInput{Started: false, OriginFocused: true, TileID: "u/1"}
	if got := DecideDrop(focused); got != DropNavigate {
		t.Errorf("bare click on focused pane = %v, want DropNavigate", got)
	}
	// A real drag acts regardless of prior focus — only the bare click is
	// focus-gated (dragging is an unambiguous intent).
	drag := DropInput{Started: true, OriginFocused: false, TileID: "u/1", HasTarget: true}
	if got := DecideDrop(drag); got != DropMove {
		t.Errorf("drag from unfocused pane = %v, want DropMove", got)
	}
}

// TestDecideDropTargetReadOnly: a drop (move OR clone) onto a read-only grid
// (the node grid, fs/proc) is rejected up front — no doomed RPC, no
// misleading "changed elsewhere" reconcile notice.
func TestDecideDropTargetReadOnly(t *testing.T) {
	base := DropInput{Started: true, TileID: "u/1", HasTarget: true, TargetReadOnly: true}
	if got := DecideDrop(base); got != DropRejected {
		t.Errorf("move onto read-only grid = %v, want DropRejected", got)
	}
	clone := base
	clone.Clone = true
	if got := DecideDrop(clone); got != DropRejected {
		t.Errorf("clone onto read-only grid = %v, want DropRejected", got)
	}
	ok := base
	ok.TargetReadOnly = false
	if got := DecideDrop(ok); got != DropMove {
		t.Errorf("move onto writable grid = %v, want DropMove", got)
	}
}

func TestMoveForbidden(t *testing.T) {
	cases := []struct {
		name             string
		sameGrid         bool
		crossPlugin      bool
		srcKind, dstKind string
		want             bool
	}{
		{"same grid, both source", true, false, "fs", "fs", false},
		{"same grid, regular", true, false, "", "", false},
		{"cross regular->regular", false, false, "", "", false},
		{"cross source->regular", false, false, "fs", "", true},
		{"cross regular->source", false, false, "", "proc", true},
		{"cross source->source (regression)", false, false, "fs", "proc", true},
		{"cross same-kind source->source", false, false, "fs", "fs", true},
		// Crossing an id namespace is not a forbidden move — it is not a
		// move at all: the left-drag becomes a LINK (DropLink), so nothing
		// is forbidden here (owner decision 2026-07-19). The source-kind
		// arms are exempted too: linking host content into a grid is the
		// mount philosophy, and a read-only destination is rejected by the
		// TargetReadOnly gate, not this one.
		{"cross-plugin left-drag is a link, not forbidden", false, true, "", "", false},
		{"cross-plugin from a source grid links too", false, true, "fs", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := MoveForbidden(c.sameGrid, c.crossPlugin, c.srcKind, c.dstKind); got != c.want {
				t.Errorf("MoveForbidden(%v, %v, %q, %q) = %v, want %v", c.sameGrid, c.crossPlugin, c.srcKind, c.dstKind, got, c.want)
			}
		})
	}
}

// TestCloneForbidden pins the right-drag policy: since issue #200 NOTHING is
// forbidden — a solid well deep-copies across plugins, a link copies as a
// link, a leaf copies bytes, and within one namespace clones always worked.
func TestCloneForbidden(t *testing.T) {
	cases := []struct {
		name                             string
		crossPlugin, isWell, isReference bool
		want                             bool
	}{
		{"solid well across namespaces (deep copy, #200)", true, true, false, false},
		{"link well across namespaces (mount, exit well)", true, true, true, false},
		{"leaf across namespaces (byte copy)", true, false, false, false},
		{"leaf link across namespaces (link copy)", true, false, true, false},
		{"solid well within one namespace (deep copy)", false, true, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CloneForbidden(c.crossPlugin, c.isWell, c.isReference); got != c.want {
				t.Errorf("CloneForbidden(%v, %v, %v) = %v, want %v", c.crossPlugin, c.isWell, c.isReference, got, c.want)
			}
		})
	}
}

func TestDecideDrop(t *testing.T) {
	// base is a clean, started, left-drag of a real tile over a valid
	// empty target cell — i.e. the DropMove case. Each row flips just the
	// fields under test so precedence is exercised in isolation.
	base := DropInput{Started: true, TileID: "7", HasTarget: true}

	cases := []struct {
		name string
		in   DropInput
		want DropAction
	}{
		// --- the happy paths ---
		{"clean left drag -> move", base, DropMove},
		{"clean right drag -> clone",
			DropInput{Started: true, TileID: "7", HasTarget: true, Clone: true}, DropClone},

		// --- early branches beat everything ---
		{"bare click on focused pane -> navigate (beats all)",
			DropInput{Started: false, OriginFocused: true, IsTemplate: true, TileID: "7", OverDelete: true, HasTarget: true}, DropNavigate},
		{"bare click on unfocused pane -> focus only (beats all)",
			DropInput{Started: false, OriginFocused: false, IsTemplate: true, TileID: "7", OverDelete: true, HasTarget: true}, DropFocusOnly},
		{"template -> create (beats pan/delete)",
			DropInput{Started: true, IsTemplate: true, TileID: "", OverDelete: true, HasTarget: true}, DropCreateTemplate},
		{"pan (tileID \"\") -> panEnd (beats delete)",
			DropInput{Started: true, TileID: "", OverDelete: true, HasTarget: true}, DropPanEnd},

		// --- the regression: delete must fire ---
		{"over delete button -> delete",
			DropInput{Started: true, TileID: "7", OverDelete: true}, DropDelete},
		{"delete wins over an occupied target (precedence)",
			DropInput{Started: true, TileID: "7", OverDelete: true, HasTarget: true, Occupied: true}, DropDelete},
		{"delete fires even with no target",
			DropInput{Started: true, TileID: "7", OverDelete: true, HasTarget: false}, DropDelete},

		// --- rejection cases, one cause each ---
		{"no target -> rejected",
			DropInput{Started: true, TileID: "7", HasTarget: false}, DropRejected},
		{"forbidden cross-grid move -> rejected",
			DropInput{Started: true, TileID: "7", HasTarget: true, Forbidden: true}, DropRejected},
		{"same cell -> rejected",
			DropInput{Started: true, TileID: "7", HasTarget: true, SameCell: true}, DropRejected},
		{"occupied -> rejected",
			DropInput{Started: true, TileID: "7", HasTarget: true, Occupied: true}, DropRejected},

		// --- clone flavor ---
		{"clone with a clean target -> clone",
			DropInput{Started: true, TileID: "7", HasTarget: true, Clone: true}, DropClone},
		// SameCell/Occupied still reject a clone (both commit paths check them).
		{"clone onto occupied -> rejected",
			DropInput{Started: true, TileID: "7", HasTarget: true, Clone: true, Occupied: true}, DropRejected},
		{"clone onto same cell -> rejected",
			DropInput{Started: true, TileID: "7", HasTarget: true, Clone: true, SameCell: true}, DropRejected},
		// A forbidden clone (solid well across namespaces; caller feeds
		// CloneForbidden) rejects like a forbidden move.
		{"forbidden clone -> rejected",
			DropInput{Started: true, TileID: "7", HasTarget: true, Clone: true, Forbidden: true}, DropRejected},

		// --- the 2026-07-19 gestures: cross-namespace left-drag is a LINK ---
		{"cross-plugin left drag -> link",
			DropInput{Started: true, TileID: "7", HasTarget: true, CrossPlugin: true}, DropLink},
		{"cross-plugin right drag -> clone (copy, not link)",
			DropInput{Started: true, TileID: "7", HasTarget: true, CrossPlugin: true, Clone: true}, DropClone},
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
		clone     bool
		want      GhostPlan
	}{
		{"delete shrinks+fragments in origin", DropDelete, false, false,
			GhostPlan{PaneID: origin, TargetCellSize: srcSz * 0.2, Fragmentation: 1.0}},
		{"rejected forbidden cross-grid: no-entry in target", DropRejected, true, false,
			GhostPlan{PaneID: target, TargetCellSize: srcSz, Forbidden: true, Cursor: "not-allowed"}},
		{"rejected off-canvas: glide back in origin, no badge", DropRejected, false, false,
			GhostPlan{PaneID: origin, TargetCellSize: srcSz}},
		{"move snaps to target cell", DropMove, false, false,
			GhostPlan{PaneID: target, TargetCellSize: tgtSz}},
		{"clone snaps to target cell", DropClone, false, true,
			GhostPlan{PaneID: target, TargetCellSize: tgtSz}},
		// The teaching signal: a cross-namespace left-drag previews as a
		// LINK (chain badge) — never as a bare move, or the source's
		// survival after the drop would read as a surprise duplicate.
		{"link snaps to target cell with the chain badge", DropLink, false, false,
			GhostPlan{PaneID: target, TargetCellSize: tgtSz, Link: true}},
	}
	for _, c := range cases {
		got := GhostPlanForDrop(c.action, c.forbidden, c.clone,
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
