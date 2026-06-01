// Package dragdrop holds the pure-math helpers used by the canvas client to
// translate cursor positions into grid cell coordinates and to validate
// proposed drops.
package dragdrop

import "math"

// Pane describes the screen rectangle and viewport state for one pane. CellPx
// is the rendered size of one cell at zoom 1.0; the actual pixel size on
// screen is CellPx*Zoom.
type Pane struct {
	ScreenX, ScreenY float64 // top-left of the pane in screen coordinates
	ScreenW, ScreenH float64
	Cx, Cy           float64 // viewport center in cells
	Zoom             float64
	CellPx           float64
}

// ScreenToCell converts (sx, sy) in screen coordinates to (cx, cy) in cell
// coordinates within the pane's viewport. Returns floating-point cells; the
// caller floors / rounds depending on context.
func (p Pane) ScreenToCell(sx, sy float64) (float64, float64) {
	// Top-left of the viewport in screen space is centered on (Cx, Cy).
	// Scale: 1 cell = CellPx * Zoom screen pixels.
	cellSize := p.CellPx * p.Zoom
	cx := p.Cx + (sx-(p.ScreenX+p.ScreenW/2))/cellSize
	cy := p.Cy + (sy-(p.ScreenY+p.ScreenH/2))/cellSize
	return cx, cy
}

// CellToScreen does the inverse mapping.
func (p Pane) CellToScreen(cx, cy float64) (float64, float64) {
	cellSize := p.CellPx * p.Zoom
	sx := p.ScreenX + p.ScreenW/2 + (cx-p.Cx)*cellSize
	sy := p.ScreenY + p.ScreenH/2 + (cy-p.Cy)*cellSize
	return sx, sy
}

// CellAt returns the integer cell containing screen point (sx, sy)
// using floor semantics (see FloorCellAt for the rationale). The
// wasm hit-testers were each building this same dragdrop.Pane +
// ScreenToCell + floor combo by hand; this one method captures it.
func (p Pane) CellAt(sx, sy float64) (int64, int64) {
	cx, cy := p.ScreenToCell(sx, sy)
	return int64(math.Floor(cx)), int64(math.Floor(cy))
}

// PaneAt returns the pane (by index) under (sx, sy) given an ordered slice
// of pane rectangles. Returns -1 if no pane covers the point. Panes are
// expected to be axis-aligned and non-overlapping.
func PaneAt(panes []Pane, sx, sy float64) int {
	for i, p := range panes {
		if sx >= p.ScreenX && sy >= p.ScreenY &&
			sx < p.ScreenX+p.ScreenW && sy < p.ScreenY+p.ScreenH {
			return i
		}
	}
	return -1
}

// SnapToCell rounds a floating-cell coordinate to the nearest whole cell.
// We round-half-down (floor + 0.5) so a drag exactly on the boundary lands
// in the lower-numbered cell. This matches typical UI expectations where
// dragging onto the leading edge "stays" rather than "advances".
//
// Use this for "where should a tile come to rest?" semantics. For "what
// cell is the cursor currently INSIDE?" use FloorCellAt — round and floor
// disagree on the lower-right half of every cell, and that mismatch will
// make hit-tests miss.
func SnapToCell(c float64) int64 {
	if c >= 0 {
		return int64(c + 0.5)
	}
	// For negative values, biasing toward zero keeps the boundary
	// behavior symmetric.
	return int64(c - 0.5)
}

// FloorCellAt returns the integer cell that contains the screen point
// (sx, sy) on a cell grid whose top-left is at (originX, originY) and
// whose cell size is cellSize screen pixels.
//
// "Floor" semantics: every interior point of cell N reports N — never
// N±1. This is the right answer for hit-testing "what tile is under
// the cursor?". SnapToCell, by contrast, rounds to the nearest cell
// boundary and is the right answer for "where should a dragged tile
// snap on release?". Mixing them up means the lower-right half of
// each cell rounds forward, so a hit-test using SnapToCell silently
// misses half of every cell.
func FloorCellAt(originX, originY, cellSize, sx, sy float64) (int64, int64) {
	return int64(math.Floor((sx - originX) / cellSize)),
		int64(math.Floor((sy - originY) / cellSize))
}

// HiddenMatch reports whether a tile should be skipped during render
// because it is currently being dragged. The drag layer paints a
// ghost following the cursor; the source's static row in the cache
// needs to be hidden underneath it so we don't see two copies.
//
// Important: matches by *tile id* (primary-key row), not by object
// lineage. Two tiles can share an ObjectID — CloneTile deliberately
// copies the source's ObjectID into the new row so the two pieces
// share a lineage. Hiding by ObjectID therefore makes every clone of
// the dragged tile vanish during the drag; hiding by row id keeps
// each clone visible.
func HiddenMatch(hiddenTileID int64, hiddenPaneID, currentPaneID string, tileID int64) bool {
	return hiddenTileID != 0 && hiddenPaneID == currentPaneID && tileID == hiddenTileID
}

// EdgeBand returns the suggested edge-band thickness in pixels for the
// pane: a fixed 80 px on large panes, but never more than 12% of the
// pane's smaller dimension so the band never swallows a tiny pane.
//
// Used by the client to decide whether a right-click is "on the edge"
// (which means ascend) vs. "in the middle" (which targets a tile or
// is a no-op).
func EdgeBand(p Pane) float64 {
	smaller := p.ScreenW
	if p.ScreenH < smaller {
		smaller = p.ScreenH
	}
	cap := smaller * 0.12
	if cap < 80 {
		return cap
	}
	return 80
}

// IsInEdgeZone reports whether (x, y) is within `band` pixels of any
// edge of pane p.
func IsInEdgeZone(p Pane, x, y, band float64) bool {
	return x-p.ScreenX < band ||
		(p.ScreenX+p.ScreenW)-x < band ||
		y-p.ScreenY < band ||
		(p.ScreenY+p.ScreenH)-y < band
}

// Side identifies one of a pane's four edges. Used by the input layer
// to translate a click position inside a pane into a split direction
// + which half the new pane should occupy.
type Side int

const (
	SideTop Side = iota
	SideBottom
	SideLeft
	SideRight
)

// ClosestEdge returns the side of the pane whose edge is nearest to
// (sx, sy). Computed via min-of-four perpendicular distances. Ties
// (e.g., the dead center of the pane) resolve to top first, then left,
// for determinism — the user said the tiebreak doesn't matter.
//
// The point need not lie inside the pane; for points outside, the
// edge they're "outside of" is the nearest one.
func ClosestEdge(p Pane, sx, sy float64) Side {
	dt := sy - p.ScreenY
	db := (p.ScreenY + p.ScreenH) - sy
	dl := sx - p.ScreenX
	dr := (p.ScreenX + p.ScreenW) - sx
	best := Side(SideTop)
	bestD := dt
	if db < bestD {
		bestD = db
		best = SideBottom
	}
	if dl < bestD {
		bestD = dl
		best = SideLeft
	}
	if dr < bestD {
		// no need to update bestD here; nothing reads it after.
		_ = bestD
		best = SideRight
	}
	return best
}

// ChildPreview describes a well's child-grid preview as drawn inside
// its parent grid. Origin is the screen coord of child cell (0, 0)
// and CellPx is the rendered size of one child cell in screen pixels.
// Use ChildPreviewFor to compute these from a well's footprint plus
// the parent pane's transform.
type ChildPreview struct {
	OriginX, OriginY float64
	CellPx           float64
}

// ChildPreviewFor returns the screen-coord transform for a well's
// child-grid preview, given the parent pane, the well's footprint &
// view region, and a resolved child-cell-per-parent-cell ratio (caller
// computes via zoomtrans.EffectiveViewZoom). previewCell = parentCell ×
// previewRatio. Pane-size independent.
func ChildPreviewFor(parent Pane, well struct {
	X, Y, W, H, ViewX, ViewY int64
}, previewRatio float64) ChildPreview {
	parentCell := parent.CellPx * parent.Zoom
	previewCell := parentCell * previewRatio
	wellLeft, wellTop := parent.CellToScreen(float64(well.X), float64(well.Y))
	wellCenterX := wellLeft + float64(well.W)*parentCell/2
	wellCenterY := wellTop + float64(well.H)*parentCell/2
	return ChildPreview{
		OriginX: wellCenterX - (float64(well.ViewX)+float64(well.W)/2)*previewCell,
		OriginY: wellCenterY - (float64(well.ViewY)+float64(well.H)/2)*previewCell,
		CellPx:  previewCell,
	}
}

// ChildCellAtScreen returns the child-grid cell coordinate (as a
// float, caller floors/rounds as needed) for a screen point inside
// the preview.
func (cp ChildPreview) ChildCellAtScreen(sx, sy float64) (float64, float64) {
	return (sx - cp.OriginX) / cp.CellPx, (sy - cp.OriginY) / cp.CellPx
}

// CellToScreen returns the screen coordinate of the top-left corner
// of child cell (cx, cy) in the preview.
func (cp ChildPreview) CellToScreen(cx, cy float64) (float64, float64) {
	return cp.OriginX + cx*cp.CellPx, cp.OriginY + cy*cp.CellPx
}

// TileContainsCell reports whether the cell (cx, cy) lies within the
// rectangle (x, y, w, h). Used to decide whether a cursor's child-cell
// hits a tile inside a well preview.
func TileContainsCell(x, y, w, h, cx, cy int64) bool {
	return cx >= x && cx < x+w && cy >= y && cy < y+h
}

// FootprintFits reports whether a (w, h)-sized footprint at (x, y) fits
// inside the pane's framed rectangle. The framed rect is computed from the
// pane's screen size, zoom, and viewport center. Used by the client to gray
// out drop targets before shipping any RPC.
func (p Pane) FootprintFits(x, y, w, h int64) bool {
	cellSize := p.CellPx * p.Zoom
	visibleW := p.ScreenW / cellSize
	visibleH := p.ScreenH / cellSize
	left := p.Cx - visibleW/2
	right := p.Cx + visibleW/2
	top := p.Cy - visibleH/2
	bottom := p.Cy + visibleH/2
	return float64(x) >= left && float64(x+w) <= right &&
		float64(y) >= top && float64(y+h) <= bottom
}

// InTileCenter reports whether the cell-space point (cellX, cellY) lies
// inside the inner 1/3 × 1/3 of the tile at (x, y, w, h). The center
// region scales with the tile so it's always 1/9 of the footprint, even
// on 1×1 tiles, and the right-button "clone grab handle" feels the same
// at every zoom.
func InTileCenter(x, y, w, h int64, cellX, cellY float64) bool {
	xf, yf := float64(x), float64(y)
	wf, hf := float64(w), float64(h)
	return cellX >= xf+wf/3 && cellX <= xf+2*wf/3 &&
		cellY >= yf+hf/3 && cellY <= yf+2*hf/3
}

// ResizeAnchors are the cell-coord state captured at the start of a
// right-button tile resize. PinX/PinY is the corner of the original
// tile diagonally opposite the click quadrant (clicking in the BR
// quadrant pins TL, etc.). OrigMovingX/OrigMovingY is the corner in
// the click quadrant — where the moving corner starts. ClickCellX/Y is
// the cell the cursor was rounded to at the click, so that mouse
// movement deltas can be translated cell-for-cell.
type ResizeAnchors struct {
	PinX, PinY               int64
	OrigMovingX, OrigMovingY int64
	ClickCellX, ClickCellY   int64
}

// ResizeAnchorsFor returns the anchors for a tile at (x, y, w, h) given
// the cursor's float cell coordinates at click time. Which quadrant the
// click lands in decides which corner is pinned (opposite) and which is
// moving (under the cursor).
func ResizeAnchorsFor(x, y, w, h int64, cellXf, cellYf float64) ResizeAnchors {
	var a ResizeAnchors
	midX := float64(x) + float64(w)/2
	midY := float64(y) + float64(h)/2
	if cellXf >= midX {
		a.PinX = x
		a.OrigMovingX = x + w
	} else {
		a.PinX = x + w
		a.OrigMovingX = x
	}
	if cellYf >= midY {
		a.PinY = y
		a.OrigMovingY = y + h
	} else {
		a.PinY = y + h
		a.OrigMovingY = y
	}
	a.ClickCellX = int64(math.Round(cellXf))
	a.ClickCellY = int64(math.Round(cellYf))
	return a
}

// ResizeFromCursor returns the proposed (x, y, w, h) for the tile given
// the cursor's current rounded cell. The moving corner is
// OrigMoving + (cur - click); the new tile is bb(pin, moving) with each
// side at least 1.
func ResizeFromCursor(a ResizeAnchors, curCellX, curCellY int64) (int64, int64, int64, int64) {
	movX := a.OrigMovingX + (curCellX - a.ClickCellX)
	movY := a.OrigMovingY + (curCellY - a.ClickCellY)
	x, w := RangeFromAnchors(a.PinX, movX, a.OrigMovingX > a.PinX)
	y, h := RangeFromAnchors(a.PinY, movY, a.OrigMovingY > a.PinY)
	return x, y, w, h
}

// RangeFromAnchors returns [start, length] of a 1-D range given a pinned
// anchor and a moving anchor in integer cells. Minimum length is 1; on
// the degenerate moving == pin case, the 1-cell range is placed on the
// side the user originally clicked (origRight) so the rectangle's
// identity stays stable across the crossover.
func RangeFromAnchors(pin, moving int64, origRight bool) (start, length int64) {
	if moving == pin {
		if origRight {
			return pin, 1
		}
		return pin - 1, 1
	}
	if moving > pin {
		return pin, moving - pin
	}
	return moving, pin - moving
}

// NearPx reports whether two pixel coordinates are within half a pixel.
// Used for "is this divider line at the same place as that one?" checks
// where exact float equality is too brittle.
func NearPx(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 0.5
}
