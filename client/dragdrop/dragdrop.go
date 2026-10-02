// Package dragdrop turns cursor positions into grid cell coordinates and
// decides what a drag release does. DecideDrop is the one verdict both the
// in-flight preview and the commit obey.
package dragdrop

import "math"

// Pane is one pane's screen rectangle and viewport. A cell is CellPx*Zoom
// pixels on screen.
type Pane struct {
	ScreenX, ScreenY float64 // top-left of the pane in screen coordinates
	ScreenW, ScreenH float64
	Cx, Cy           float64 // viewport center in cells
	Zoom             float64
	CellPx           float64
}

// ScreenToCell returns floating-point cells; the caller floors or rounds.
func (p Pane) ScreenToCell(sx, sy float64) (float64, float64) {
	cellSize := p.CellPx * p.Zoom
	cx := p.Cx + (sx-(p.ScreenX+p.ScreenW/2))/cellSize
	cy := p.Cy + (sy-(p.ScreenY+p.ScreenH/2))/cellSize
	return cx, cy
}

func (p Pane) CellToScreen(cx, cy float64) (float64, float64) {
	cellSize := p.CellPx * p.Zoom
	sx := p.ScreenX + p.ScreenW/2 + (cx-p.Cx)*cellSize
	sy := p.ScreenY + p.ScreenH/2 + (cy-p.Cy)*cellSize
	return sx, sy
}

// Shows reports whether the cell box at (x, y), w by h, reaches into the pane:
// the one cull, so what is drawn and what is said to be shown agree.
func (p Pane) Shows(x, y, w, h float64) bool {
	left, top := p.CellToScreen(x, y)
	cellSize := p.CellPx * p.Zoom
	return left+w*cellSize >= p.ScreenX && top+h*cellSize >= p.ScreenY &&
		left <= p.ScreenX+p.ScreenW && top <= p.ScreenY+p.ScreenH
}

// CellAt floors; see FloorCellAt.
func (p Pane) CellAt(sx, sy float64) (int64, int64) {
	cx, cy := p.ScreenToCell(sx, sy)
	return int64(math.Floor(cx)), int64(math.Floor(cy))
}

// SnapToCell rounds halves away from zero, so the snap is symmetric about
// zero. It is where a dragged tile comes to rest; FloorCellAt is which cell
// the cursor is inside.
func SnapToCell(c float64) int64 {
	if c >= 0 {
		return int64(c + 0.5)
	}
	return int64(c - 0.5)
}

// FloorCellAt reports N for every interior point of cell N, which is what a
// hit-test needs; SnapToCell would miss the lower-right half of every cell.
func FloorCellAt(originX, originY, cellSize, sx, sy float64) (int64, int64) {
	return int64(math.Floor((sx - originX) / cellSize)),
		int64(math.Floor((sy - originY) / cellSize))
}

// HiddenMatch reports whether a tile is skipped during render because it is
// being dragged. It matches by tile id: matching on anything a clone shares
// with its source would make every clone vanish during the drag.
func HiddenMatch(hiddenTileID string, hiddenPaneID, currentPaneID string, tileID string) bool {
	return hiddenTileID != "" && hiddenPaneID == currentPaneID && tileID == hiddenTileID
}

// ChildPreview's Origin is the screen coordinate of child cell (0, 0).
type ChildPreview struct {
	OriginX, OriginY float64
	CellPx           float64
}

// ChildPreviewFor takes previewRatio, child cells per parent cell, from the
// caller's zoomtrans.EffectiveViewZoom. The result is independent of pane size.
func ChildPreviewFor(parent Pane, well struct {
	X, Y, W, H     int64
	ViewCx, ViewCy float64
}, previewRatio float64) ChildPreview {
	parentCell := parent.CellPx * parent.Zoom
	previewCell := parentCell * previewRatio
	wellLeft, wellTop := parent.CellToScreen(float64(well.X), float64(well.Y))
	wellCenterX := wellLeft + float64(well.W)*parentCell/2
	wellCenterY := wellTop + float64(well.H)*parentCell/2
	return ChildPreview{
		OriginX: wellCenterX - well.ViewCx*previewCell,
		OriginY: wellCenterY - well.ViewCy*previewCell,
		CellPx:  previewCell,
	}
}

func (cp ChildPreview) ChildCellAtScreen(sx, sy float64) (float64, float64) {
	return (sx - cp.OriginX) / cp.CellPx, (sy - cp.OriginY) / cp.CellPx
}

func (cp ChildPreview) CellToScreen(cx, cy float64) (float64, float64) {
	return cp.OriginX + cx*cp.CellPx, cp.OriginY + cy*cp.CellPx
}

func TileContainsCell(x, y, w, h, cx, cy int64) bool {
	return cx >= x && cx < x+w && cy >= y && cy < y+h
}

// RectsOverlap is the predicate the server's overlap check applies, so the
// drop preflight and PlaceTile cannot disagree about a collision.
func RectsOverlap(ax, ay, aw, ah, bx, by, bw, bh int64) bool {
	return ax < bx+bw && bx < ax+aw && ay < by+bh && by < ay+ah
}

// InTileCenter scales with the tile, so the copy and link handle feels the
// same at every zoom and on a 1x1 tile.
func InTileCenter(x, y, w, h int64, cellX, cellY float64) bool {
	xf, yf := float64(x), float64(y)
	wf, hf := float64(w), float64(h)
	return cellX >= xf+wf/3 && cellX <= xf+2*wf/3 &&
		cellY >= yf+hf/3 && cellY <= yf+2*hf/3
}

// ResizeAnchors is captured when a right-button tile resize starts. Pin is the
// corner opposite the click quadrant and ClickCell the cursor's rounded cell,
// so a delta translates cell for cell.
type ResizeAnchors struct {
	PinX, PinY               int64
	OrigMovingX, OrigMovingY int64
	ClickCellX, ClickCellY   int64
}

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

// ResizeFromCursor keeps each side at least 1.
func ResizeFromCursor(a ResizeAnchors, curCellX, curCellY int64) (int64, int64, int64, int64) {
	movX := a.OrigMovingX + (curCellX - a.ClickCellX)
	movY := a.OrigMovingY + (curCellY - a.ClickCellY)
	x, w := RangeFromAnchors(a.PinX, movX, a.OrigMovingX > a.PinX)
	y, h := RangeFromAnchors(a.PinY, movY, a.OrigMovingY > a.PinY)
	return x, y, w, h
}

// RangeFromAnchors puts the range on the side first clicked when the anchors
// meet, so the rectangle keeps its identity across the crossover.
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

// MoveForbidden reports whether the server would reject a same-namespace
// cross-grid left-drag because either end declares host_content: host content
// cannot migrate into Gridwell and host-side mv is not implemented.
func MoveForbidden(sameGrid, crossPlugin, srcHost, dstHost bool) bool {
	if sameGrid || crossPlugin {
		return false
	}
	return srcHost || dstHost
}

// Intent is fixed by the press that arms the drag and never re-derived, so the
// preview and the commit cannot disagree with the gesture the user started.
type Intent int

const (
	// IntentMove is a left-drag, and the zero value a palette template drag
	// leaves unset.
	IntentMove Intent = iota
	IntentCopy        // a right-drag
	IntentLink        // ctrl with a right-drag
)

// Creates: a copy and a link both do, so the source stays put and is a
// neighbor the drop must not land on, and MoveForbidden does not apply.
func (i Intent) Creates() bool { return i != IntentMove }

// DropAction is the verdict for a release and its preview. The commit and the
// ghost both route through DecideDrop, so they cannot drift apart.
type DropAction int

const (
	// DropNavigate is a bare click in any pane: descend, ascend or select,
	// placing nothing.
	DropNavigate      DropAction = iota
	DropNavigateSplit            // that click with ctrl held at press
	DropCreateTemplate
	DropPanEnd   // an empty-space drag, which persists the viewport
	DropDelete   // a release over the source pane's trashcan button
	DropRejected // snaps back
	DropMove
	DropClone
	// DropLink creates a reference, an exit well for a grid and a leaf link
	// otherwise. The source is untouched and identity never migrates.
	DropLink
)

// DropInput is every world-read a drop decision needs, gathered once before
// any teardown clears the drag state. It holds no App fields and no js.Value,
// so a cleared field can never be read late. Occupied excludes the moving tile
// on a move, mirroring the server's PlaceTile.
type DropInput struct {
	Started bool
	// SplitNav is ctrl at left-press time, fixed there so releasing it
	// mid-click cannot change the verdict.
	SplitNav   bool
	IsTemplate bool
	Intent     Intent
	TileID     string // "" for a pan or empty-space drag
	OverDelete bool
	HasTarget  bool
	Forbidden  bool
	// TargetReadOnly rejects an arrival before the RPC. A same-grid
	// left-drag is placement, not creation, and is exempt.
	TargetReadOnly bool
	SameGrid       bool
	SameCell       bool
	Occupied       bool
	// CrossPlugin: the grids are in different id namespaces, so a left-drag
	// verdicts DropLink and a right-drag stays DropClone.
	CrossPlugin bool
}

// DecideDrop's branch order is the decision: an earlier arm wins. HasTarget
// sits above every arm that lands something in a grid, so none commits against
// a destination the target resolution refused; only the pan and the trashcan,
// which land in no grid, stand above it.
func DecideDrop(in DropInput) DropAction {
	switch {
	case !in.Started && in.SplitNav:
		return DropNavigateSplit
	case !in.Started:
		return DropNavigate
	case in.TileID == "" && !in.IsTemplate:
		return DropPanEnd
	case in.OverDelete:
		return DropDelete
	case !in.HasTarget:
		return DropRejected
	case in.IsTemplate:
		return DropCreateTemplate
	case in.Forbidden:
		return DropRejected
	case in.TargetReadOnly && !(in.SameGrid && !in.Intent.Creates()):
		return DropRejected
	case in.SameCell:
		return DropRejected
	case in.Occupied:
		return DropRejected
	case in.Intent == IntentCopy:
		return DropClone
	case in.Intent == IntentLink || in.CrossPlugin:
		return DropLink
	default:
		return DropMove
	}
}

// GhostPlan is how the in-flight drag ghost renders for a drop verdict.
type GhostPlan struct {
	PaneID         string  // pane whose coordinate space the ghost rests in
	TargetCellSize float64 // size the ghost lerps toward
	Fragmentation  float64 // 1 shatters into the trashcan
	Forbidden      bool    // draw the no-entry badge
	// Link draws the dashed ghost and chain badge. Without it a cross-plugin
	// left-drag would look like a move and the survivor read as a duplicate.
	Link   bool
	Cursor string // CSS cursor: "" or "not-allowed"
}

// GhostPlanForDrop takes both pane ids and cell sizes because the ghost rests
// in a different pane per verdict. SameCell and Occupied get no style: the
// preview is optimistic and the commit does the authoritative overlap check.
func GhostPlanForDrop(action DropAction, forbidden bool,
	originPaneID, targetPaneID string, srcCellSize, targetCellSize float64) GhostPlan {
	switch action {
	case DropDelete:
		return GhostPlan{PaneID: originPaneID, TargetCellSize: srcCellSize * 0.2, Fragmentation: 1.0}
	case DropLink:
		return GhostPlan{PaneID: targetPaneID, TargetCellSize: targetCellSize, Link: true}
	case DropRejected:
		if forbidden {
			return GhostPlan{PaneID: targetPaneID, TargetCellSize: srcCellSize, Forbidden: true, Cursor: "not-allowed"}
		}
		return GhostPlan{PaneID: originPaneID, TargetCellSize: srcCellSize}
	default: // DropMove / DropClone
		return GhostPlan{PaneID: targetPaneID, TargetCellSize: targetCellSize}
	}
}

// PromoteToWell excludes the dragged tile: a well dropped into its own subtree
// is a cycle the server rejects. The caller resolves isWell from
// rpc.IsWellKind, keeping api/rpc out of here.
func PromoteToWell(isWell bool, childGridID, tileID, draggedTileID string) bool {
	return isWell && childGridID != "" && tileID != draggedTileID
}
