//go:build js && wasm

package main

import (
	"math"
	"syscall/js"

	"github.com/josephburnett/gridwell/client/dragdrop"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// colorTileResize is the preview outline of an in-flight tile resize.
// Same blue as active split and swap so the "active gesture" feel is
// consistent.
const colorTileResize = "#4a6fff"

// Preview rendering colors.
const (
	colorSplitInactive = "#6c6f78" // grey
	colorSplitActive   = "#4a6fff" // bright blue (matches focus)
	colorSwapArrow     = "#4a6fff"
	colorCloseWarn     = "#e0727a" // red
)

// resizeBandPx is the thickness of the resize zone near each pane
// edge — the right-button "grab the divider" zone. Replaces the old
// dividerHit hit-band; the entire interaction model is region-based
// now.
const resizeBandPx = 10.0

// rightCloseThreshold is the minimum size (in screen px) a pane side
// must have along the resize axis to *not* be closed at release. Two
// resize bands wide so the gesture reads as "you can't shrink past a
// pane that's only resize zones."
const rightCloseThreshold = 2 * resizeBandPx

// rightDragKind classifies an in-flight right-button gesture. Set on
// mousedown; the tile-vs-pane fork is decided by where the cursor
// landed (over a tile → tile gesture; otherwise → pane gesture). Never
// changes mid-gesture.
type rightDragKind int

const (
	rightDragNone rightDragKind = iota
	rightDragSwap
	rightDragSplit
	rightDragResize
	// rightDragTileCenter is armed when right-down lands in the inner
	// 1/3 × 1/3 of a tile (cell coords). Release here commits cap/redig
	// for wells, or fill if the well's child grid is empty. Files: no
	// preview, no commit. Drag out of center suspends the preview;
	// drag back re-engages.
	rightDragTileCenter
	// rightDragTileResize is armed when right-down lands on a tile
	// outside its center. The pin is the corner of the original tile
	// diagonally opposite the click quadrant; the cursor (snapped to
	// cell) defines the moving corner. New footprint = bounding box of
	// (pin, cursor) with each side >= 1. Pin can be crossed mid-drag.
	rightDragTileResize
)

// rightDragState carries everything the move and up handlers need to
// finish (or cancel) the gesture. One discriminated struct keeps the
// frame-tick bookkeeping in one place.
type rightDragState struct {
	kind           rightDragKind
	startX, startY float64
	curX, curY     float64

	// Swap-only.
	originPaneID string

	// Split-only.
	splitPaneID string
	splitPane   paneRect
	splitSide   pane.Side

	// Resize-only.
	targetSplit *pane.Split
	splitDir    pane.Direction
	container   paneRect

	// Tile-only.
	tilePaneID string
	tileNode   rpc.Tile
	tilePane   *pane.Pane // for path/grid lookups at commit time
	tilePaneR  paneRect   // pane rect at right-down (for cell mapping)

	// rightDragTileCenter-only. cursorInCenter tracks whether the
	// cursor is currently inside the center zone, so the preview can
	// disable when dragged out and re-engage when dragged back.
	cursorInCenter bool

	// rightDragTileResize-only. The pin is the corner of the original
	// tile diagonally opposite the click quadrant; it stays put. The
	// moving corner starts at the original tile's other diagonal
	// corner, then translates by (cursor_cell - click_cell) on each
	// move — so on right-down with no movement the new tile equals
	// the original. New tile = bb(pin, moving) with min 1×1; the pin
	// can be crossed (tile flips through it).
	pinX, pinY               int64
	origMovingX, origMovingY int64
	clickCellX, clickCellY   int64
	tileNewX, tileNewY       int64
	tileNewW, tileNewH       int64
}

// onRightDown classifies the right-down and arms the matching gesture
// state. Tile gestures (cap/redig/fill on bare release; resize on
// drag) take priority when the cursor is over a tile in a grid view.
// No tree or store edits happen here — those wait for release (or the
// next move tick, for pane resize).
//
// Caller has already verified there's no animation in flight and
// (sx, sy) is over pane p with screen rect r.
func (a *App) onRightDown(p *pane.Pane, r paneRect, sx, sy float64) {
	// Tile gesture: only valid in a grid view (not file mode).
	if p.FileFocus == 0 {
		if n := a.tileAtScreen(p, r, sx, sy); n != nil {
			a.armTileGesture(p, r, n, sx, sy)
			return
		}
	}
	region := pane.ClassifyRegion(paneRectToRect(r), resizeBandPx, sx, sy)
	switch {
	case region.IsResize():
		// Resize zones map to the divider on that side, if any. If
		// there is no divider (the pane abuts the screen edge), fall
		// through to split — keeps the gesture useful.
		if d := a.dividerOnSide(p, region.Side()); d != nil {
			a.rightDrag = &rightDragState{
				kind:        rightDragResize,
				startX:      sx,
				startY:      sy,
				curX:        sx,
				curY:        sy,
				targetSplit: d.Split,
				splitDir:    d.Dir,
				container:   paneRect{X: d.ContainerRect.X, Y: d.ContainerRect.Y, W: d.ContainerRect.W, H: d.ContainerRect.H},
			}
			return
		}
		// No divider on that side — fall through to split.
		a.rightDrag = &rightDragState{
			kind:        rightDragSplit,
			startX:      sx,
			startY:      sy,
			curX:        sx,
			curY:        sy,
			splitPaneID: p.ID,
			splitPane:   r,
			splitSide:   region.Side(),
		}
	case region.IsSwap():
		a.rightDrag = &rightDragState{
			kind:         rightDragSwap,
			startX:       sx,
			startY:       sy,
			curX:         sx,
			curY:         sy,
			originPaneID: p.ID,
		}
	case region.IsSplit():
		a.rightDrag = &rightDragState{
			kind:        rightDragSplit,
			startX:      sx,
			startY:      sy,
			curX:        sx,
			curY:        sy,
			splitPaneID: p.ID,
			splitPane:   r,
			splitSide:   region.Side(),
		}
	}
}

// onRightMove updates the cursor position and applies live changes.
// Pane-resize is the only gesture that mutates the tree mid-drag; the
// rest render previews that commit on release.
func (a *App) onRightMove(sx, sy float64) {
	rd := a.rightDrag
	if rd == nil {
		return
	}
	rd.curX = sx
	rd.curY = sy
	switch rd.kind {
	case rightDragResize:
		newRatio := pane.RatioFromCursor(paneRectToRect(rd.container), rd.splitDir, sx, sy)
		rd.targetSplit.Ratio = newRatio
	case rightDragTileCenter:
		rd.cursorInCenter = inTileCenter(&rd.tileNode, rd.tilePane, rd.tilePaneR, sx, sy)
		a.advanceCloneDrag(sx, sy)
	case rightDragTileResize:
		rd.tileNewX, rd.tileNewY, rd.tileNewW, rd.tileNewH = tileResizeFromPin(rd, sx, sy)
	}
	a.draw()
}

// tileAtScreen returns the tile under (sx, sy) inside pane p, or nil.
// Wraps cellAtScreen + tileAtCell so the tile-gesture entry is a
// single helper rather than an inline pair.
func (a *App) tileAtScreen(p *pane.Pane, r paneRect, sx, sy float64) *rpc.Tile {
	cellX, cellY := cellAtScreen(p, r, sx, sy)
	return a.tileAtCell(p, cellX, cellY)
}

// armTileGesture installs the right state for a right-button-down on
// a tile. The model is the same for every tile kind (well, file, URL,
// black hole):
//   - Center 1/3 × 1/3: rightDragTileCenter — drag-past-threshold
//     clones the tile (via the standard a.dragging machinery, armed
//     in parallel); bare release does nothing.
//   - Outside center: rightDragTileResize — rubber-band the footprint.
//
// In both cases drawRightDragPreview paints the 5-zone hotspot
// overlay so the user can see all the affordances on the tile at a
// glance.
func (a *App) armTileGesture(p *pane.Pane, r paneRect, n *rpc.Tile, sx, sy float64) {
	common := rightDragState{
		startX:     sx,
		startY:     sy,
		curX:       sx,
		curY:       sy,
		tilePaneID: p.ID,
		tileNode:   *n,
		tilePane:   p,
		tilePaneR:  r,
	}
	if inTileCenter(n, p, r, sx, sy) {
		common.kind = rightDragTileCenter
		common.cursorInCenter = true
		a.rightDrag = &common
		a.armRightClone(p, r, n, sx, sy)
	} else {
		common.kind = rightDragTileResize
		common.pinX, common.pinY,
			common.origMovingX, common.origMovingY,
			common.clickCellX, common.clickCellY = tileResizeAnchors(n, p, r, sx, sy)
		common.tileNewX = n.X
		common.tileNewY = n.Y
		common.tileNewW = n.W
		common.tileNewH = n.H
		a.rightDrag = &common
	}
	a.draw()
}

// inTileCenter reports whether (sx, sy) falls inside the inner
// 1/3 × 1/3 cell rect of tile n. Computed in cell coordinates so the
// zone scales with zoom and is always 1/9 of the tile's footprint
// even on 1×1 tiles.
func inTileCenter(n *rpc.Tile, p *pane.Pane, r paneRect, sx, sy float64) bool {
	ps := dragdrop.Pane{
		ScreenX: r.X, ScreenY: r.Y, ScreenW: r.W, ScreenH: r.H,
		Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom, CellPx: cellPx,
	}
	cx, cy := ps.ScreenToCell(sx, sy)
	w := float64(n.W)
	h := float64(n.H)
	x := float64(n.X)
	y := float64(n.Y)
	return cx >= x+w/3 && cx <= x+2*w/3 && cy >= y+h/3 && cy <= y+2*h/3
}

// tileResizeAnchors returns the cell-coord anchors used by the rubber-
// band resize math:
//   - pin: corner of the original tile diagonally opposite the click
//     quadrant (clicking in the BR quadrant pins TL, etc.).
//   - origMoving: corner of the original tile in the click quadrant —
//     where the moving corner starts at right-down.
//   - clickCell: cell that the cursor is in at click time, rounded.
//
// The move handler tracks the moving corner as
// origMoving + (cursorCell - clickCell), so right-down with no
// movement leaves the tile unchanged.
func tileResizeAnchors(n *rpc.Tile, p *pane.Pane, r paneRect, sx, sy float64) (
	pinX, pinY, origMovingX, origMovingY, clickCellX, clickCellY int64,
) {
	ps := dragdrop.Pane{
		ScreenX: r.X, ScreenY: r.Y, ScreenW: r.W, ScreenH: r.H,
		Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom, CellPx: cellPx,
	}
	cxF, cyF := ps.ScreenToCell(sx, sy)
	midX := float64(n.X) + float64(n.W)/2
	midY := float64(n.Y) + float64(n.H)/2
	if cxF >= midX {
		pinX = n.X
		origMovingX = n.X + n.W
	} else {
		pinX = n.X + n.W
		origMovingX = n.X
	}
	if cyF >= midY {
		pinY = n.Y
		origMovingY = n.Y + n.H
	} else {
		pinY = n.Y + n.H
		origMovingY = n.Y
	}
	clickCellX = int64(math.Round(cxF))
	clickCellY = int64(math.Round(cyF))
	return
}

// tileResizeFromPin computes the proposed (X, Y, W, H) for the
// current cursor position. The moving corner is
// origMoving + (cursorCell - clickCell); the new tile is
// bb(pin, moving) with each side at least 1.
func tileResizeFromPin(rd *rightDragState, sx, sy float64) (int64, int64, int64, int64) {
	ps := dragdrop.Pane{
		ScreenX: rd.tilePaneR.X, ScreenY: rd.tilePaneR.Y,
		ScreenW: rd.tilePaneR.W, ScreenH: rd.tilePaneR.H,
		Cx: rd.tilePane.Cx, Cy: rd.tilePane.Cy,
		Zoom: rd.tilePane.Zoom, CellPx: cellPx,
	}
	cxF, cyF := ps.ScreenToCell(sx, sy)
	curCellX := int64(math.Round(cxF))
	curCellY := int64(math.Round(cyF))
	movX := rd.origMovingX + (curCellX - rd.clickCellX)
	movY := rd.origMovingY + (curCellY - rd.clickCellY)
	x, w := rangeFromAnchors(rd.pinX, movX, rd.origMovingX > rd.pinX)
	y, h := rangeFromAnchors(rd.pinY, movY, rd.origMovingY > rd.pinY)
	return x, y, w, h
}

// rangeFromAnchors returns the [start, length] of the rectangle along
// one axis given a fixed pin and a moving anchor in cell coords. Min
// length is 1; when moving == pin (degenerate), the 1-cell range is
// placed on the side `origRight` — i.e., the side the user originally
// clicked — so the result remains stable across the crossover.
func rangeFromAnchors(pin, moving int64, origRight bool) (start, length int64) {
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

// finishRightDrag commits or cancels the in-flight gesture at the
// release coordinates and clears the drag state.
func (a *App) finishRightDrag(sx, sy float64) {
	rd := a.rightDrag
	if rd == nil {
		return
	}
	a.rightDrag = nil
	rd.curX = sx
	rd.curY = sy

	switch rd.kind {
	case rightDragResize:
		a.commitResize(rd, sx, sy)
	case rightDragSwap:
		a.commitSwap(rd, sx, sy)
	case rightDragSplit:
		a.commitSplit(rd, sx, sy)
	case rightDragTileCenter:
		a.commitTileCenter(rd, sx, sy)
	case rightDragTileResize:
		a.commitTileResize(rd)
	}
	a.draw()
	a.scheduleURLUpdate()
}

// advanceCloneDrag drives the a.dragging ghost from a right-button
// move. Same logic the left-drag onMouseMove path runs, narrowed to
// the case we care about (d.tileID != 0, d.clone = true): cross the
// drag threshold to materialize the ghost, then track the cursor.
func (a *App) advanceCloneDrag(sx, sy float64) {
	d := a.dragging
	if d == nil {
		return
	}
	if !d.started {
		dxs := sx - d.startScreenX
		dys := sy - d.startScreenY
		if dxs*dxs+dys*dys < dragThreshold*dragThreshold {
			d.curScreenX = sx
			d.curScreenY = sy
			return
		}
		d.started = true
		size := d.srcCellSize
		if size <= 0 {
			size = cellPx
		}
		a.ghost = &ghost{
			tile:              d.snapshotTile,
			paneID:            d.originPaneID,
			screenX:           d.originScreenX,
			screenY:           d.originScreenY,
			displayedCellSize: size,
			targetCellSize:    size,
		}
	}
	if a.ghost != nil {
		if t, ok := a.dropTargetAt(sx, sy, d.tileID); ok {
			a.ghost.paneID = t.pane.ID
			a.ghost.targetCellSize = t.cellSize
		} else {
			a.ghost.paneID = d.originPaneID
			a.ghost.targetCellSize = d.srcCellSize
		}
		size := a.ghost.displayedCellSize
		a.ghost.screenX = sx - d.cellOffsetX*size
		a.ghost.screenY = sy - d.cellOffsetY*size
	}
	d.curScreenX = sx
	d.curScreenY = sy
}

// armRightClone primes the a.dragging state so a drag from the center
// zone of tile n will clone it. The ghost itself is materialized only
// once the cursor moves past dragThreshold — same convention as
// left-click drags — so a bare right-click leaves the world unchanged.
//
// We don't hide the original tile (clone=true), and the cursor offset
// inside the tile is preserved so the grab point tracks the cursor.
func (a *App) armRightClone(p *pane.Pane, r paneRect, n *rpc.Tile, sx, sy float64) {
	ps := dragdrop.Pane{
		ScreenX: r.X, ScreenY: r.Y, ScreenW: r.W, ScreenH: r.H,
		Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom, CellPx: cellPx,
	}
	cxF, cyF := ps.ScreenToCell(sx, sy)
	tlX, tlY := ps.CellToScreen(float64(n.X), float64(n.Y))
	cellSize := cellPx * p.Zoom
	a.dragging = &dragState{
		originPaneID:   p.ID,
		tileID:         n.ID,
		clone:          true,
		startScreenX:   sx,
		startScreenY:   sy,
		curScreenX:     sx,
		curScreenY:     sy,
		cellOffsetX:    cxF - float64(n.X),
		cellOffsetY:    cyF - float64(n.Y),
		snapshotTile:   *n,
		originScreenX:  tlX,
		originScreenY:  tlY,
		originPaneRect: r,
		srcGridID:      a.gridIDForPath(p.Path),
		srcPath:        append([]int64(nil), p.Path...),
		srcViewRect:    a.paneViewRect(p, ps),
		srcCellSize:    cellSize,
	}
}

// commitTileCenter handles release of a center-zone gesture. With the
// unified mouse-only model the bare-release semantics are: no-op. The
// center is "the clone grab handle" — clone happens by dragging past
// the threshold. If a.dragging was promoted to "started" by motion,
// commit it as a CloneTile drop; otherwise just clear the priming
// state so subsequent clicks aren't affected.
func (a *App) commitTileCenter(rd *rightDragState, sx, sy float64) {
	_ = rd
	d := a.dragging
	a.dragging = nil
	if d == nil || !d.started {
		a.ghost = nil
		a.draw()
		return
	}
	a.commitRightClone(d, sx, sy)
}

// commitRightClone resolves the drop target at (sx, sy) and either
// fires CloneTile, drops the source onto a black-hole sink (which is
// a delete), or snap-backs the ghost on a rejected drop. The async
// RPC fires in a goroutine; the local cache is patched optimistically
// by the SSE event when it lands.
func (a *App) commitRightClone(d *dragState, sx, sy float64) {
	t, ok := a.dropTargetAt(sx, sy, d.tileID)
	if !ok {
		a.cancelDragSnapBack(d)
		return
	}
	// Black-hole sink: any tile dropped on a black-hole tile is
	// deleted. The drop coordinates are inside the black hole's
	// footprint, so the dragged source disappears rather than landing.
	if sink := a.tileAtCellInTarget(t, sx, sy); sink != nil && sink.IsBlackHole() {
		a.runDeleteTile(d, t)
		a.ghost = nil
		a.draw()
		return
	}
	dropX, dropY := t.cellAtCursor(sx, sy, d.cellOffsetX, d.cellOffsetY)
	if t.gridID == d.srcGridID && dropX == d.snapshotTile.X && dropY == d.snapshotTile.Y {
		a.cancelDragSnapBack(d)
		return
	}
	if a.nodeAtCellInGrid(t.gridID, dropX, dropY) != nil {
		a.cancelDragSnapBack(d)
		return
	}
	targetX := t.originX + float64(dropX)*t.cellSize
	targetY := t.originY + float64(dropY)*t.cellSize
	if a.ghost != nil {
		a.ghost.paneID = t.pane.ID
		a.ghost.targetCellSize = t.cellSize
	}
	a.startSnap(targetX, targetY, snapMs)
	srcPath := append([]int64(nil), d.srcPath...)
	dstPath := append([]int64(nil), t.path...)
	srcView := d.srcViewRect
	dstView := t.viewRect
	dstGridID := t.gridID
	srcGridID := d.srcGridID
	tileID := d.tileID
	go func() {
		req := rpc.CloneTileRequest{
			Path: rpc.Path{WellIDs: srcPath}, ViewRect: srcView,
			TileID:     tileID,
			DestGridID: dstGridID, DestPath: rpc.Path{WellIDs: dstPath}, DestViewRect: dstView,
			X: dropX, Y: dropY,
		}
		var resp rpc.TileResponse
		status, _ := postJSON("/rpc/CloneTile", req, &resp)
		if status != 200 {
			a.snapBackToOrigin(d)
			return
		}
		a.fetchGrid(srcGridID)
		a.fetchGrid(dstGridID)
	}()
}

// runDeleteTile fires DeleteTile against the dragged source tile. Used
// when the right-button-clone gesture drops onto a black-hole sink.
func (a *App) runDeleteTile(d *dragState, t *dropTarget) {
	srcPath := append([]int64(nil), d.srcPath...)
	srcView := d.srcViewRect
	srcGridID := d.srcGridID
	tileID := d.tileID
	go func() {
		req := rpc.DeleteTileRequest{
			Path: rpc.Path{WellIDs: srcPath}, ViewRect: srcView,
			TileID: tileID,
		}
		var resp rpc.DeleteTileResponse
		_, _ = postJSON("/rpc/DeleteTile", req, &resp)
		a.fetchGrid(srcGridID)
		if t != nil && t.gridID != srcGridID {
			a.fetchGrid(t.gridID)
		}
	}()
}

// tileAtCellInTarget returns the tile under the cursor inside the
// resolved drop target's grid (which may be a well's child grid), or
// nil. Mirrors tileAtCell but works against an arbitrary cached grid.
func (a *App) tileAtCellInTarget(t *dropTarget, sx, sy float64) *rpc.Tile {
	cellX := dragdrop.SnapToCell((sx - t.originX) / t.cellSize)
	cellY := dragdrop.SnapToCell((sy - t.originY) / t.cellSize)
	return a.nodeAtCellInGrid(t.gridID, cellX, cellY)
}

// commitTileResize commits the proposed (X, Y, W, H) via ResizeTile.
// If the new size matches the original, no RPC is issued.
func (a *App) commitTileResize(rd *rightDragState) {
	n := rd.tileNode
	if rd.tileNewX == n.X && rd.tileNewY == n.Y && rd.tileNewW == n.W && rd.tileNewH == n.H {
		return
	}
	p := a.tree.FindPane(rd.tilePaneID)
	if p == nil {
		return
	}
	pscreen := dragdrop.Pane{
		ScreenX: rd.tilePaneR.X, ScreenY: rd.tilePaneR.Y,
		ScreenW: rd.tilePaneR.W, ScreenH: rd.tilePaneR.H,
		Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom, CellPx: cellPx,
	}
	view := a.paneViewRect(p, pscreen)
	gid := a.gridIDForPath(p.Path)
	req := rpc.ResizeTileRequest{
		Path:     rpc.Path{WellIDs: p.Path},
		ViewRect: view,
		TileID:   n.ID,
		X:        rd.tileNewX,
		Y:        rd.tileNewY,
		W:        rd.tileNewW,
		H:        rd.tileNewH,
	}
	go func() {
		var resp rpc.TileResponse
		_, _ = postJSON("/rpc/ResizeTile", req, &resp)
		a.fetchGrid(gid)
	}()
}

// commitResize applies the final ratio and, if either side is below
// rightCloseThreshold, collapses that side.
func (a *App) commitResize(rd *rightDragState, sx, sy float64) {
	newRatio := pane.RatioFromCursor(paneRectToRect(rd.container), rd.splitDir, sx, sy)
	rd.targetSplit.Ratio = newRatio
	var aSize, bSize float64
	if rd.splitDir == pane.Horizontal {
		aSize = rd.container.H * newRatio
		bSize = rd.container.H * (1 - newRatio)
	} else {
		aSize = rd.container.W * newRatio
		bSize = rd.container.W * (1 - newRatio)
	}
	switch {
	case aSize < rightCloseThreshold:
		_ = a.tree.CollapseSplit(rd.targetSplit, true)
	case bSize < rightCloseThreshold:
		_ = a.tree.CollapseSplit(rd.targetSplit, false)
	}
}

// commitSwap exchanges the origin pane with whatever pane the cursor
// is over at release. Same-pane release = no-op (cancel). Off-canvas
// release = no-op.
func (a *App) commitSwap(rd *rightDragState, sx, sy float64) {
	destPane, _, ok := a.paneAtScreen(sx, sy)
	if !ok || destPane.ID == rd.originPaneID {
		return
	}
	_ = a.tree.Swap(rd.originPaneID, destPane.ID)
	_ = a.tree.SetFocus(rd.originPaneID)
}

// commitSplit converts the in-flight preview into a real split. The
// gesture is committed iff the cursor:
//  1. is past the start point in the *expected* direction (away from
//     the chosen edge), and
//  2. the resulting split position is in the valid range — both child
//     panes get at least 2*resizeBandPx of the relevant dimension.
//
// Anything else is cancelled silently.
func (a *App) commitSplit(rd *rightDragState, sx, sy float64) {
	if !splitGestureActive(rd, sx, sy) {
		return
	}
	pos, ok := splitClampedPosition(rd, sx, sy)
	if !ok {
		return
	}
	ratio := splitRatioFromPos(rd, pos)
	p := a.tree.FindPane(rd.splitPaneID)
	if p == nil {
		return
	}
	_ = a.tree.SetFocus(p.ID)
	if _, err := a.tree.SplitOnSideAt(rd.splitSide, ratio); err != nil {
		return
	}
}

// dividerOnSide returns the Divider directly adjacent to pane p on
// the requested side, or nil if pane abuts the screen edge with no
// sibling on that side.
func (a *App) dividerOnSide(p *pane.Pane, side pane.Side) *pane.Divider {
	root := pane.Rect{X: 0, Y: 0, W: a.width, H: a.height}
	r := paneRectFor(a, p)
	divs := pane.Dividers(a.tree, root, resizeBandPx)
	for i := range divs {
		d := divs[i]
		// Match by adjacency: the divider's rect must touch the pane's
		// edge on the requested side.
		switch side {
		case pane.SideTop:
			if d.Dir == pane.Horizontal && near(d.Rect.Y+d.Rect.H/2, r.Y) {
				return &divs[i]
			}
		case pane.SideBottom:
			if d.Dir == pane.Horizontal && near(d.Rect.Y+d.Rect.H/2, r.Y+r.H) {
				return &divs[i]
			}
		case pane.SideLeft:
			if d.Dir == pane.Vertical && near(d.Rect.X+d.Rect.W/2, r.X) {
				return &divs[i]
			}
		case pane.SideRight:
			if d.Dir == pane.Vertical && near(d.Rect.X+d.Rect.W/2, r.X+r.W) {
				return &divs[i]
			}
		}
	}
	return nil
}

// near is a small float-tolerance equality check for pixel
// comparisons.
func near(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 0.5
}

// paneRectToRect converts the wasm-local paneRect to pane.Rect.
func paneRectToRect(r paneRect) pane.Rect {
	return pane.Rect{X: r.X, Y: r.Y, W: r.W, H: r.H}
}

// splitGestureActive reports whether the current cursor position
// represents an "active" split — past the start in the expected
// direction. Direction is determined by the chosen side: top trapezoid
// expects downward drag, left expects rightward, etc.
func splitGestureActive(rd *rightDragState, sx, sy float64) bool {
	switch rd.splitSide {
	case pane.SideTop:
		return sy > rd.startY
	case pane.SideBottom:
		return sy < rd.startY
	case pane.SideLeft:
		return sx > rd.startX
	case pane.SideRight:
		return sx < rd.startX
	}
	return false
}

// splitClampedPosition returns the cursor's projection onto the
// split's axis, clamped to the valid range. The bool is false when
// the cursor is outside the valid range — in that case the line
// renders grey at the clamped position and a release commits nothing.
//
// "Valid range" leaves at least 2*resizeBandPx on each side so both
// resulting panes can hold a full resize-band frame.
func splitClampedPosition(rd *rightDragState, sx, sy float64) (float64, bool) {
	r := rd.splitPane
	switch rd.splitSide {
	case pane.SideTop, pane.SideBottom:
		minY := r.Y + 2*resizeBandPx
		maxY := r.Y + r.H - 2*resizeBandPx
		if minY >= maxY {
			return 0, false // pane too small to split at all
		}
		if sy < minY {
			return minY, false
		}
		if sy > maxY {
			return maxY, false
		}
		return sy, true
	case pane.SideLeft, pane.SideRight:
		minX := r.X + 2*resizeBandPx
		maxX := r.X + r.W - 2*resizeBandPx
		if minX >= maxX {
			return 0, false
		}
		if sx < minX {
			return minX, false
		}
		if sx > maxX {
			return maxX, false
		}
		return sx, true
	}
	return 0, false
}

// drawRightDragPreview paints the in-flight gesture's visual hint:
//   - Split: a horizontal/vertical line at the clamped cursor projection.
//     Blue when "active" (past start in expected direction AND in
//     valid range), grey otherwise.
//   - Swap: a double-headed arrow from origin pane center to either
//     the cursor or the destination pane center.
//   - Resize: a red border on the side that would close on release,
//     so the user can drag back before letting go.
func (a *App) drawRightDragPreview() {
	rd := a.rightDrag
	if rd == nil {
		return
	}
	switch rd.kind {
	case rightDragSplit:
		a.drawSplitPreview(rd)
	case rightDragSwap:
		a.drawSwapPreview(rd)
	case rightDragResize:
		a.drawResizeCloseWarning(rd)
	case rightDragTileCenter:
		a.drawTileHotspotOverlay(rd)
	case rightDragTileResize:
		a.drawTileHotspotOverlay(rd)
		a.drawTileResizePreview(rd)
	}
}

// drawTileHotspotOverlay paints the five-zone affordance overlay over
// the tile while a right-button gesture is in flight (or just primed,
// before any drag movement). The user sees the four edge zones with
// resize arrows and the center zone with a clone glyph. Strictly grey
// — the overlay is informational only.
func (a *App) drawTileHotspotOverlay(rd *rightDragState) {
	left, top, w, h := tileScreenRect(&rd.tileNode, rd.tilePane, rd.tilePaneR)
	if w <= 0 || h <= 0 {
		return
	}
	// Inner thirds in screen coords.
	tw := w / 3
	th := h / 3
	cxL := left + tw
	cxR := left + 2*tw
	cyT := top + th
	cyB := top + 2*th

	a.cctx.Set("strokeStyle", colorMuted)
	a.cctx.Set("fillStyle", colorMuted)
	a.cctx.Set("lineWidth", 1.0)

	// Outline the tile and the inner-third grid.
	a.cctx.Call("strokeRect", left+0.5, top+0.5, w-1, h-1)
	a.cctx.Call("beginPath")
	a.cctx.Call("moveTo", cxL+0.5, top)
	a.cctx.Call("lineTo", cxL+0.5, top+h)
	a.cctx.Call("moveTo", cxR+0.5, top)
	a.cctx.Call("lineTo", cxR+0.5, top+h)
	a.cctx.Call("moveTo", left, cyT+0.5)
	a.cctx.Call("lineTo", left+w, cyT+0.5)
	a.cctx.Call("moveTo", left, cyB+0.5)
	a.cctx.Call("lineTo", left+w, cyB+0.5)
	a.cctx.Call("stroke")

	// Center: clone glyph (two overlapping squares).
	ccx := left + w/2
	ccy := top + h/2
	gs := math.Min(tw, th) * 0.35
	if gs < 8 {
		gs = math.Min(tw, th) * 0.5
	}
	a.cctx.Call("strokeRect", ccx-gs/2, ccy-gs/2, gs, gs)
	a.cctx.Call("strokeRect", ccx-gs/2+gs*0.25, ccy-gs/2+gs*0.25, gs, gs)

	// Edge arrows pointing outward from each face.
	arrow := math.Min(tw, th) * 0.3
	if arrow < 8 {
		arrow = 8
	}
	// Top
	drawHotspotArrow(a.cctx, left+w/2, top+th/2, 0, -arrow)
	// Bottom
	drawHotspotArrow(a.cctx, left+w/2, top+h-th/2, 0, arrow)
	// Left
	drawHotspotArrow(a.cctx, left+tw/2, top+h/2, -arrow, 0)
	// Right
	drawHotspotArrow(a.cctx, left+w-tw/2, top+h/2, arrow, 0)
}

// drawHotspotArrow draws a simple line+head from (cx, cy) in direction
// (dx, dy). The head sits at the far end.
func drawHotspotArrow(c js.Value, cx, cy, dx, dy float64) {
	hx := cx + dx
	hy := cy + dy
	c.Call("beginPath")
	c.Call("moveTo", cx, cy)
	c.Call("lineTo", hx, hy)
	c.Call("stroke")
	// Arrow head: rotate ±2.5 rad off the direction.
	ang := math.Atan2(dy, dx)
	const headLen = 5.0
	c.Call("beginPath")
	c.Call("moveTo", hx, hy)
	c.Call("lineTo", hx+math.Cos(ang+2.5)*headLen, hy+math.Sin(ang+2.5)*headLen)
	c.Call("moveTo", hx, hy)
	c.Call("lineTo", hx+math.Cos(ang-2.5)*headLen, hy+math.Sin(ang-2.5)*headLen)
	c.Call("stroke")
}

// tileScreenRect returns the on-screen rectangle of tile n as drawn
// in pane p. Mirrors the math used by the parent-grid renderer.
func tileScreenRect(n *rpc.Tile, p *pane.Pane, r paneRect) (left, top, w, h float64) {
	ps := dragdrop.Pane{
		ScreenX: r.X, ScreenY: r.Y, ScreenW: r.W, ScreenH: r.H,
		Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom, CellPx: cellPx,
	}
	left, top = ps.CellToScreen(float64(n.X), float64(n.Y))
	cellSize := cellPx * p.Zoom
	w = float64(n.W) * cellSize
	h = float64(n.H) * cellSize
	return
}

// drawTileResizePreview outlines the proposed new footprint in the
// pane's screen coordinates. The original tile keeps painting in
// place, so the preview is the new rectangle as a dashed blue stroke.
func (a *App) drawTileResizePreview(rd *rightDragState) {
	ps := dragdrop.Pane{
		ScreenX: rd.tilePaneR.X, ScreenY: rd.tilePaneR.Y,
		ScreenW: rd.tilePaneR.W, ScreenH: rd.tilePaneR.H,
		Cx: rd.tilePane.Cx, Cy: rd.tilePane.Cy,
		Zoom: rd.tilePane.Zoom, CellPx: cellPx,
	}
	left, top := ps.CellToScreen(float64(rd.tileNewX), float64(rd.tileNewY))
	cellSize := cellPx * rd.tilePane.Zoom
	w := float64(rd.tileNewW) * cellSize
	h := float64(rd.tileNewH) * cellSize
	a.cctx.Set("strokeStyle", colorTileResize)
	a.cctx.Set("lineWidth", 2.0)
	a.cctx.Call("setLineDash", jsArray(6, 4))
	a.cctx.Call("strokeRect", left, top, w, h)
	a.cctx.Call("setLineDash", jsArray())
	a.cctx.Set("lineWidth", 1.0)
}

// jsArray makes a JS array from variadic float64 args. Used to set
// dash patterns on the canvas 2D context.
func jsArray(vals ...float64) js.Value {
	arr := make([]any, len(vals))
	for i, v := range vals {
		arr[i] = v
	}
	return js.ValueOf(arr)
}

// drawSplitPreview draws the grey/blue partition line. The line
// always renders at the clamped position (so it never leaves the
// valid range), but its color reflects whether a release here would
// commit.
func (a *App) drawSplitPreview(rd *rightDragState) {
	pos, valid := splitClampedPosition(rd, rd.curX, rd.curY)
	active := valid && splitGestureActive(rd, rd.curX, rd.curY)
	color := colorSplitInactive
	if active {
		color = colorSplitActive
	}
	a.cctx.Set("strokeStyle", color)
	a.cctx.Set("lineWidth", 2.0)
	a.cctx.Call("beginPath")
	r := rd.splitPane
	switch rd.splitSide {
	case pane.SideTop, pane.SideBottom:
		// Horizontal divider line at y=pos, full pane width.
		a.cctx.Call("moveTo", r.X, pos)
		a.cctx.Call("lineTo", r.X+r.W, pos)
	case pane.SideLeft, pane.SideRight:
		// Vertical divider at x=pos, full pane height.
		a.cctx.Call("moveTo", pos, r.Y)
		a.cctx.Call("lineTo", pos, r.Y+r.H)
	}
	a.cctx.Call("stroke")
	a.cctx.Set("lineWidth", 1.0)
}

// drawSwapPreview draws a double-headed arrow from origin pane center
// to either the destination pane center (if cursor is over a
// different pane) or the cursor position.
func (a *App) drawSwapPreview(rd *rightDragState) {
	originPane := a.tree.FindPane(rd.originPaneID)
	if originPane == nil {
		return
	}
	originRect := paneRectFor(a, originPane)
	x1 := originRect.X + originRect.W/2
	y1 := originRect.Y + originRect.H/2

	x2, y2 := rd.curX, rd.curY
	destPane, destRect, ok := a.paneAtScreen(rd.curX, rd.curY)
	dimColor := false
	if ok && destPane.ID != rd.originPaneID {
		// Snap the arrow tip to the destination pane center for
		// clarity that THAT pane is the swap target.
		x2 = destRect.X + destRect.W/2
		y2 = destRect.Y + destRect.H/2
	} else {
		// Same pane (or off-canvas) → no swap will happen. Dim the
		// arrow so the user knows.
		dimColor = true
	}
	color := colorSwapArrow
	if dimColor {
		color = colorSplitInactive
	}
	a.cctx.Set("strokeStyle", color)
	a.cctx.Set("fillStyle", color)
	a.cctx.Set("lineWidth", 2.0)
	a.cctx.Call("beginPath")
	a.cctx.Call("moveTo", x1, y1)
	a.cctx.Call("lineTo", x2, y2)
	a.cctx.Call("stroke")
	a.cctx.Set("lineWidth", 1.0)

	// Arrowheads at both ends.
	angle := math.Atan2(y2-y1, x2-x1)
	const arrowLen = 12.0
	drawArrowHead(a, x1, y1, angle+math.Pi, arrowLen)
	drawArrowHead(a, x2, y2, angle, arrowLen)
}

// drawArrowHead paints a small filled triangle at (cx, cy) pointing
// in the direction `angle` (radians). Used by the swap preview.
func drawArrowHead(a *App, cx, cy, angle, size float64) {
	tipX := cx + math.Cos(angle)*size
	tipY := cy + math.Sin(angle)*size
	leftX := cx + math.Cos(angle+2.5)*size
	leftY := cy + math.Sin(angle+2.5)*size
	rightX := cx + math.Cos(angle-2.5)*size
	rightY := cy + math.Sin(angle-2.5)*size
	a.cctx.Call("beginPath")
	a.cctx.Call("moveTo", tipX, tipY)
	a.cctx.Call("lineTo", leftX, leftY)
	a.cctx.Call("lineTo", rightX, rightY)
	a.cctx.Call("closePath")
	a.cctx.Call("fill")
}

// drawResizeCloseWarning highlights the about-to-close side of the
// active resize divider with a red border, so the user can drag back
// before releasing if they didn't intend to close.
func (a *App) drawResizeCloseWarning(rd *rightDragState) {
	r := rd.container
	var aSize, bSize float64
	ratio := rd.targetSplit.Ratio
	if rd.splitDir == pane.Horizontal {
		aSize = r.H * ratio
		bSize = r.H * (1 - ratio)
	} else {
		aSize = r.W * ratio
		bSize = r.W * (1 - ratio)
	}
	closeA := aSize < rightCloseThreshold
	closeB := bSize < rightCloseThreshold
	if !closeA && !closeB {
		return
	}
	// Find the rect of the about-to-close subtree by recomputing the
	// split's children rects. The Layout helpers already do this, so
	// reuse them for consistency.
	aRect, bRect := splitChildRects(rd.container, rd.splitDir, ratio)
	target := bRect
	if closeA {
		target = aRect
	}
	a.cctx.Set("strokeStyle", colorCloseWarn)
	a.cctx.Set("lineWidth", paneBorderPx)
	half := paneBorderPx / 2
	a.cctx.Call("strokeRect", target.X+half, target.Y+half, target.W-paneBorderPx, target.H-paneBorderPx)
	a.cctx.Set("lineWidth", 1.0)
}

// splitChildRects mirrors pane.splitRect (which is unexported) for
// use by the close-warning renderer. Inlined here rather than
// exporting the package helper since the scope is small.
func splitChildRects(container paneRect, dir pane.Direction, ratio float64) (a, b paneRect) {
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	if dir == pane.Horizontal {
		hA := container.H * ratio
		a = paneRect{X: container.X, Y: container.Y, W: container.W, H: hA}
		b = paneRect{X: container.X, Y: container.Y + hA, W: container.W, H: container.H - hA}
		return
	}
	wA := container.W * ratio
	a = paneRect{X: container.X, Y: container.Y, W: wA, H: container.H}
	b = paneRect{X: container.X + wA, Y: container.Y, W: container.W - wA, H: container.H}
	return
}

// splitRatioFromPos converts a clamped split position back into the
// ratio the new pane should occupy.
func splitRatioFromPos(rd *rightDragState, pos float64) float64 {
	r := rd.splitPane
	switch rd.splitSide {
	case pane.SideTop:
		return (pos - r.Y) / r.H
	case pane.SideBottom:
		return ((r.Y + r.H) - pos) / r.H
	case pane.SideLeft:
		return (pos - r.X) / r.W
	case pane.SideRight:
		return ((r.X + r.W) - pos) / r.W
	}
	return 0.5
}
