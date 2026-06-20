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

// SnapToCell rounds a floating-cell coordinate to the nearest whole cell,
// rounding halves away from zero — so an exact boundary value snaps to the
// cell of larger magnitude: 0.5 -> 1, 1.5 -> 2, -0.5 -> -1, -1.5 -> -2.
//
// Use this for "where should a tile come to rest?" semantics. For "what
// cell is the cursor currently INSIDE?" use FloorCellAt — round and floor
// disagree on the lower-right half of every cell, and that mismatch will
// make hit-tests miss.
func SnapToCell(c float64) int64 {
	if c >= 0 {
		return int64(c + 0.5)
	}
	// Negative values round the same way by magnitude (e.g. -0.5 -> -1), so
	// the snap is symmetric about zero.
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

// MoveForbidden reports whether a left-drag (move) from a tile in a grid whose
// source kind is srcKind to a destination grid whose source kind is dstKind
// would be rejected by the server. "" is a regular Gridwell grid; a non-empty
// kind (fs / proc) is source-backed.
//
// It mirrors the server's MoveTile rule exactly: a *cross-grid* move is
// forbidden when EITHER endpoint is source-backed — a host file can't migrate
// into Gridwell, regular tiles can't move into a host directory, and host-side
// mv between two source dirs isn't implemented (clone/right-drag links
// instead). A same-grid move never crosses the boundary, so it's always
// allowed. (The UI previously used an XOR here, which wrongly invited a
// source→source cross-grid drop that the server then rejected.)
func MoveForbidden(sameGrid bool, srcKind, dstKind string) bool {
	if sameGrid {
		return false
	}
	return srcKind != "" || dstKind != ""
}

// DropAction is the single verdict for a drag release (and the matching
// in-flight preview). BOTH the commit handlers and the ghost-preview
// handlers in the wasm client route through DecideDrop so they can never
// disagree — the trashcan-delete regression (commit 92f9b21) was exactly
// a preview/commit disagreement caused by reading a torn-down field on
// the commit side only.
type DropAction int

const (
	// DropNavigate: a bare click (no drag started) — the wasm side runs
	// descent/ascent/selection. Not a placement at all.
	DropNavigate DropAction = iota
	// DropCreateTemplate: a palette-swatch drag — create a fresh tile at
	// the snapped cell.
	DropCreateTemplate
	// DropPanEnd: an empty-space (tileID==0) drag — just persist viewport.
	DropPanEnd
	// DropDelete: released over the source pane's + (trashcan) button.
	// This is the regression branch.
	DropDelete
	// DropEmbed: released over a raw-text descent — insert a markdown
	// reference instead of moving the tile.
	DropEmbed
	// DropRejected: nothing legal here (read-only doc, no target,
	// forbidden cross-grid move, same cell, or occupied) — snap back.
	DropRejected
	// DropMove: a clean left-drag — MoveTile.
	DropMove
	// DropClone: a clean right-drag — CloneTile.
	DropClone
)

// DropInput is the snapshot of every world-read a drop decision needs,
// gathered ONCE at release (or per preview frame) BEFORE any teardown
// nils out drag state. It holds no App fields and no js.Value — that is
// the whole point: gather first, then decide, so a cleared field can
// never be read late.
//
// Field provenance in the wasm caller (impure resolvers stay there):
//   - OverDelete: a.overDeleteButton(d, sx, sy)
//   - OverDoc:    a.docDropTargetAt(sx, sy)
//   - DocReject:  a.docRejectAt(sx, sy)
//   - HasTarget:  a.dropTargetAt(sx, sy, tileID) resolved
//   - Forbidden:  move-only — !Clone && a.dropForbiddenForMove(d, t)
//   - SameCell:   target grid == source grid && drop cell == source cell
//   - Occupied:   a.nodeAtCellInGrid(t.gridID, dropX, dropY) != nil
type DropInput struct {
	Started    bool
	IsTemplate bool
	Clone      bool  // right-drag armed
	TileID     int64 // 0 = pan / empty-space drag
	OverDelete bool
	OverDoc    bool
	DocReject  bool
	HasTarget  bool
	Forbidden  bool
	SameCell   bool
	Occupied   bool
}

// DecideDrop maps a gathered DropInput to the single action both preview
// and commit obey.
//
// The order is canonical and load-bearing — it mirrors the left-drag
// commit (onMouseUp) and reconciles the right-drag commit
// (commitRightClone), which is behavior-preserving because the two never
// actually contend: the + (trashcan) button lives in a grid-pane corner
// while OverDoc/DocReject describe a *different* pane that is a text
// descent, so OverDelete and OverDoc/DocReject are mutually exclusive in
// practice. Earlier branches strictly win:
//
//  1. !Started      → Navigate     (bare click beats everything)
//  2. IsTemplate    → CreateTemplate
//  3. TileID == 0   → PanEnd
//  4. OverDelete    → Delete
//  5. OverDoc       → Embed
//  6. DocReject     → Rejected      (read-only rendered doc)
//  7. !HasTarget    → Rejected
//  8. Forbidden     → Rejected      (cross-grid move; move-only input)
//  9. SameCell      → Rejected
//  10. Occupied     → Rejected
//  11. else         → Clone ? DropClone : DropMove
func DecideDrop(in DropInput) DropAction {
	switch {
	case !in.Started:
		return DropNavigate
	case in.IsTemplate:
		return DropCreateTemplate
	case in.TileID == 0:
		return DropPanEnd
	case in.OverDelete:
		return DropDelete
	case in.OverDoc:
		return DropEmbed
	case in.DocReject:
		return DropRejected
	case !in.HasTarget:
		return DropRejected
	case in.Forbidden:
		return DropRejected
	case in.SameCell:
		return DropRejected
	case in.Occupied:
		return DropRejected
	case in.Clone:
		return DropClone
	default:
		return DropMove
	}
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
