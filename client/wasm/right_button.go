//go:build js && wasm

package main

import (
	"context"
	"math"
	"slices"

	"github.com/josephburnett/gridwell/client/dragdrop"
	"github.com/josephburnett/gridwell/client/gesture"
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
	// 1/3 × 1/3 of a tile (cell coords). It's the clone grab handle:
	// dragging past the threshold materializes a clone ghost via the
	// standard a.dragging machinery; bare release is a no-op.
	rightDragTileCenter
	// rightDragTileResize is armed when right-down lands on a tile
	// outside its center. The pin is the corner of the original tile
	// diagonally opposite the click quadrant; the cursor (snapped to
	// cell) defines the moving corner. New footprint = bounding box of
	// (pin, cursor) with each side >= 1. Pin can be crossed mid-drag.
	rightDragTileResize
	// rightDragURLRefresh is armed when right-down lands anywhere in
	// the content area of a pane that is descended into a URL tile.
	// Dragging downward past urlRefreshThresholdPx commits the gesture
	// on release, opening (or reopening) the live URL stream. Dragging
	// back above the start point cancels. This is the "go live" gesture
	// for URL tiles; drag upward or release before threshold to cancel.
	rightDragURLRefresh
	// rightDragEmbedHint is armed when right-down lands on a rendered
	// tile-embed inside a text descent. Drag does nothing; release does
	// nothing. The sole purpose is to surface the chain-link icon so the
	// user discovers that "this is a reference, not a tile."
	rightDragEmbedHint
	// rightDragAscend is armed when right-down lands on the corner circle
	// (the +/refresh/back button) of a pane that has somewhere to ascend
	// to. Release inside the circle ascends; dragging out of the circle
	// cancels (cursorInCircle tracks this so the preview can show the
	// armed/cancel state). This is the discoverable ascent gesture; the
	// middle button is the shortcut.
	rightDragAscend
)

// urlRefreshThresholdPx is the downward drag distance (in screen pixels)
// required to arm the "release to refresh" state of the URL refresh
// gesture. 60 px ≈ one grid cell at the default zoom — large enough
// that an accidental jitter won't commit a live session, but small
// enough to feel responsive.
const urlRefreshThresholdPx = 60.0

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
	splitPane   pane.Rect
	splitSide   pane.Side

	// Resize-only.
	targetSplit *pane.Split
	splitDir    pane.Direction
	container   pane.Rect

	// Tile-only.
	tilePaneID string
	tileNode   rpc.Tile
	tilePane   *pane.Pane // for path/grid lookups at commit time
	tilePaneR  pane.Rect  // pane rect at right-down (for cell mapping)

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

	// rightDragURLRefresh-only. refreshTileID is the URL tile being
	// refreshed; refreshPaneID is the pane it lives in. Committed on
	// release when curY > startY + urlRefreshThresholdPx.
	refreshTileID int64
	refreshPaneID string

	// rightDragEmbedHint-only. embedRect is the screen rectangle of the
	// rendered tile-embed under the cursor; the chain-link glyph paints
	// centered inside it.
	embedRect [4]float64

	// rightDragAscend-only. ascendPaneID is the pane to ascend; the
	// release ascends only if cursorInCircle is still true (the cursor is
	// inside the corner circle), otherwise the gesture is cancelled.
	ascendPaneID   string
	cursorInCircle bool
}

// onRightDown classifies the right-down and arms the matching gesture
// state. Tile gestures (cap/redig/fill on bare release; resize on
// drag) take priority when the cursor is over a tile in a grid view.
// No tree or store edits happen here — those wait for release (or the
// next move tick, for pane resize).
//
// Caller has already verified there's no animation in flight and
// (sx, sy) is over pane p with screen rect r.
func (a *App) onRightDown(p *pane.Pane, r pane.Rect, sx, sy float64) {
	// Resolve the facts gesture.Classify orders, holding onto the lookups
	// (embed hit, tile, divider, region) so the arming switch can reuse
	// them instead of recomputing. Every lookup here is a pure read; the
	// state edits happen only in the arming switch below.
	in := gesture.Input{
		OnCornerCircle: pointInPlus(r, sx, sy),
		CanAscend:      a.canAscend(p),
		URLDescent:     a.isURLDescent(p),
		InURLCenter:    pointInURLCenter(r, sx, sy),
		InGridView:     p.TextFocus == 0,
		Region:         pane.ClassifyRegion(r, resizeBandPx, sx, sy),
	}

	var hit *embedHit
	if p.TextFocus != 0 && p.TextMode == rpc.TextModeRendered {
		hit = a.embedHitAt(p.ID, sx, sy)
		in.OverEmbed = hit != nil
	}

	var tile *rpc.Tile
	if in.InGridView {
		tile = a.tileAtScreen(p, r, sx, sy)
		in.OverTile = tile != nil
		if tile != nil {
			in.InTileCenter = inTileCenter(tile, p, r, sx, sy)
		}
	}

	var divider *pane.Divider
	if in.Region.IsResize() {
		divider = a.dividerOnSide(p, in.Region.Side())
		in.HasDividerOnSide = divider != nil
	}

	switch gesture.Classify(in) {
	case gesture.EmbedHint:
		// No drag, no commit — the gesture exists only to surface the
		// chain-link glyph while the button is held.
		a.rightDrag = &rightDragState{
			kind:      rightDragEmbedHint,
			startX:    sx,
			startY:    sy,
			curX:      sx,
			curY:      sy,
			embedRect: [4]float64{hit.x, hit.y, hit.w, hit.h},
		}
		a.draw()
	case gesture.Ascend:
		a.rightDrag = &rightDragState{
			kind:           rightDragAscend,
			startX:         sx,
			startY:         sy,
			curX:           sx,
			curY:           sy,
			ascendPaneID:   p.ID,
			cursorInCircle: true,
		}
		a.draw()
	case gesture.URLRefresh:
		gid := a.gridIDForPath(p.Path)
		if g, ok := a.c.Grid(gid); ok {
			if t, ok := g.Tiles[p.TextFocus]; ok {
				a.rightDrag = &rightDragState{
					kind:          rightDragURLRefresh,
					startX:        sx,
					startY:        sy,
					curX:          sx,
					curY:          sy,
					refreshTileID: t.ID,
					refreshPaneID: p.ID,
				}
				a.draw()
			}
		}
	case gesture.TileCenter, gesture.TileResize:
		// armTileGesture re-derives center-vs-resize via dragdrop and arms
		// the matching state (and primes the clone ghost for the center).
		a.armTileGesture(p, r, tile, sx, sy)
	case gesture.Resize:
		a.rightDrag = &rightDragState{
			kind:         rightDragResize,
			startX:       sx,
			startY:       sy,
			curX:         sx,
			curY:         sy,
			originPaneID: p.ID,
			targetSplit:  divider.Split,
			splitDir:     divider.Dir,
			container:    pane.Rect{X: divider.ContainerRect.X, Y: divider.ContainerRect.Y, W: divider.ContainerRect.W, H: divider.ContainerRect.H},
		}
	case gesture.Swap:
		a.rightDrag = &rightDragState{
			kind:         rightDragSwap,
			startX:       sx,
			startY:       sy,
			curX:         sx,
			curY:         sy,
			originPaneID: p.ID,
		}
	case gesture.Split:
		a.rightDrag = &rightDragState{
			kind:        rightDragSplit,
			startX:      sx,
			startY:      sy,
			curX:        sx,
			curY:        sy,
			splitPaneID: p.ID,
			splitPane:   r,
			splitSide:   in.Region.Side(),
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
		newRatio := pane.RatioFromCursor(rd.container, rd.splitDir, sx, sy)
		rd.targetSplit.Ratio = newRatio
	case rightDragTileCenter:
		rd.cursorInCenter = inTileCenter(&rd.tileNode, rd.tilePane, rd.tilePaneR, sx, sy)
		a.advanceCloneDrag(sx, sy)
	case rightDragTileResize:
		rd.tileNewX, rd.tileNewY, rd.tileNewW, rd.tileNewH = tileResizeFromPin(rd, sx, sy)
	case rightDragURLRefresh:
		// No mid-drag mutation — the preview indicator is drawn in
		// drawRightDragPreview; nothing changes in the tree until release.
	case rightDragAscend:
		// Track whether the cursor is still over the circle so the
		// preview can show armed-vs-cancel, and release knows what to do.
		pr := a.paneRectByID(rd.ascendPaneID)
		rd.cursorInCircle = pr.W > 0 && pointInPlus(pr, sx, sy)
	}
	a.draw()
}

// tileAtScreen returns the tile under (sx, sy) inside pane p, or nil.
// Wraps cellAtScreen + tileAtCell so the tile-gesture entry is a
// single helper rather than an inline pair.
func (a *App) tileAtScreen(p *pane.Pane, r pane.Rect, sx, sy float64) *rpc.Tile {
	cellX, cellY := cellAtScreen(p, r, sx, sy)
	return a.tileAtCell(p, cellX, cellY)
}

// armTileGesture installs the right state for a right-button-down on
// a tile. The model is the same for every tile kind (well, text, URL,
// blackhole):
//   - Center 1/3 × 1/3: rightDragTileCenter — drag-past-threshold
//     clones the tile (via the standard a.dragging machinery, armed
//     in parallel); bare release does nothing.
//   - Outside center: rightDragTileResize — rubber-band the footprint.
//
// In both cases drawRightDragPreview paints the 5-zone hotspot
// overlay so the user can see all the affordances on the tile at a
// glance.
func (a *App) armTileGesture(p *pane.Pane, r pane.Rect, n *rpc.Tile, sx, sy float64) {
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

// inTileCenter is a wasm-side adapter that builds the dragdrop.Pane
// from a pane.Pane + pane.Rect and delegates to dragdrop.InTileCenter
// (where the geometry lives, natively tested).
func inTileCenter(n *rpc.Tile, p *pane.Pane, r pane.Rect, sx, sy float64) bool {
	ps := paneToDragdrop(p, r)
	cx, cy := ps.ScreenToCell(sx, sy)
	return dragdrop.InTileCenter(n.X, n.Y, n.W, n.H, cx, cy)
}

// tileResizeAnchors is the wasm-side adapter for dragdrop.ResizeAnchorsFor.
func tileResizeAnchors(n *rpc.Tile, p *pane.Pane, r pane.Rect, sx, sy float64) (
	pinX, pinY, origMovingX, origMovingY, clickCellX, clickCellY int64,
) {
	ps := paneToDragdrop(p, r)
	cxF, cyF := ps.ScreenToCell(sx, sy)
	a := dragdrop.ResizeAnchorsFor(n.X, n.Y, n.W, n.H, cxF, cyF)
	return a.PinX, a.PinY, a.OrigMovingX, a.OrigMovingY, a.ClickCellX, a.ClickCellY
}

// tileResizeFromPin is the wasm-side adapter for dragdrop.ResizeFromCursor.
func tileResizeFromPin(rd *rightDragState, sx, sy float64) (int64, int64, int64, int64) {
	ps := paneToDragdrop(rd.tilePane, rd.tilePaneR)
	cxF, cyF := ps.ScreenToCell(sx, sy)
	curCellX := int64(math.Round(cxF))
	curCellY := int64(math.Round(cyF))
	a := dragdrop.ResizeAnchors{
		PinX: rd.pinX, PinY: rd.pinY,
		OrigMovingX: rd.origMovingX, OrigMovingY: rd.origMovingY,
		ClickCellX: rd.clickCellX, ClickCellY: rd.clickCellY,
	}
	return dragdrop.ResizeFromCursor(a, curCellX, curCellY)
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
	case rightDragURLRefresh:
		a.commitURLRefresh(rd, sx, sy)
	case rightDragEmbedHint:
		// No-op: the gesture only existed to surface the chain-link glyph
		// while the button was held. Release just clears it.
	case rightDragAscend:
		// Commit only if the cursor is still inside the circle; dragging
		// out of it cancels.
		if rd.cursorInCircle {
			if p := a.tree.FindPane(rd.ascendPaneID); p != nil {
				a.ascendPane(p)
			}
		}
	}
	a.draw()
	a.scheduleURLUpdate()
}

// advanceCloneDrag drives the a.dragging ghost from a right-button
// move. Same logic the left-drag onMouseMove path runs, narrowed to
// the case we care about (d.tileID != 0): cross the drag threshold
// to materialize the ghost, then track the cursor.
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
		a.ghost.overDoc = false
		if dt, ok := a.docDropTargetAt(sx, sy); ok {
			a.ghost.paneID = dt.pane.ID
			a.ghost.targetCellSize = d.srcCellSize
			a.ghost.targetFragmentation = 0.0
			a.ghost.overDoc = true
			a.canvas.Get("style").Set("cursor", "")
		} else if a.docRejectAt(sx, sy) {
			a.ghost.paneID = d.originPaneID
			a.ghost.targetCellSize = d.srcCellSize
			a.ghost.targetFragmentation = 0.0
			a.canvas.Get("style").Set("cursor", "not-allowed")
		} else if t, ok := a.dropTargetAt(sx, sy, d.tileID); ok {
			a.canvas.Get("style").Set("cursor", "")
			a.ghost.paneID = t.pane.ID
			// Dropping on a black hole deletes, whichever button armed the
			// drag (CLAUDE.md: "drag a tile onto a black hole"). Mirror the
			// left-drag delete preview — the ghost shrinks and fragments.
			sink := a.tileAtCellInTarget(t, sx, sy)
			if sink != nil && sink.Kind == rpc.KindBlackHole && sink.ID != d.tileID {
				a.ghost.targetCellSize = t.cellSize * 0.2
				a.ghost.targetFragmentation = 1.0
			} else {
				a.ghost.targetCellSize = t.cellSize
				a.ghost.targetFragmentation = 0.0
			}
		} else {
			a.canvas.Get("style").Set("cursor", "")
			a.ghost.paneID = d.originPaneID
			a.ghost.targetCellSize = d.srcCellSize
			a.ghost.targetFragmentation = 0.0
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
func (a *App) armRightClone(p *pane.Pane, r pane.Rect, n *rpc.Tile, sx, sy float64) {
	ps := paneToDragdrop(p, r)
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
		srcPath:        slices.Clone(p.Path),
		srcCellSize:    cellSize,
	}
}

// commitTileCenter handles release of a center-zone gesture. With the
// unified mouse-only model the bare-release semantics are: no-op. The
// center is "the clone grab handle" — clone happens by dragging past
// the threshold. If a.dragging was promoted to "started" by motion,
// commit it as a CloneTile drop; otherwise just clear the priming
// state so subsequent clicks aren't affected.
func (a *App) commitTileCenter(_ *rightDragState, sx, sy float64) {
	d := a.dragging
	a.dragging = nil
	if d == nil || !d.started {
		a.ghost = nil
		a.draw()
		return
	}
	a.commitRightClone(d, sx, sy)
}

// commitRightClone resolves the drop target at (sx, sy) and either deletes
// (drop on a black hole), fires CloneTile, inserts a markdown reference into
// a doc, or snap-backs the ghost on a rejected drop. The async RPC fires in
// a goroutine; the local cache is patched by the SSE event when it lands.
// Dropping on a black hole deletes regardless of button — the gesture is
// button-agnostic per CLAUDE.md; otherwise right-drag is clone-or-link.
func (a *App) commitRightClone(d *dragState, sx, sy float64) {
	// Doc drop: a raw-mode text descent under the cursor turns the gesture
	// into "insert markdown reference". The source tile is left in place
	// (clone semantics), the doc gains a link.
	if dt, ok := a.docDropTargetAt(sx, sy); ok {
		a.commitEmbedDrop(d, dt)
		a.cancelDragSnapBack(d)
		return
	}
	t, ok := a.dropTargetAt(sx, sy, d.tileID)
	if !ok {
		a.cancelDragSnapBack(d)
		return
	}
	// Black-hole sink: dropping on a black hole deletes the grabbed tile,
	// regardless of which button armed the drag — the same gesture and
	// outcome as the left-drag delete path. (The drop ghost previewed this
	// in advanceCloneDrag.)
	if sink := a.tileAtCellInTarget(t, sx, sy); sink != nil && sink.Kind == rpc.KindBlackHole && sink.ID != d.tileID {
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
	srcPath := slices.Clone(d.srcPath)
	dstPath := slices.Clone(t.path)
	dstGridID := t.gridID
	srcGridID := d.srcGridID
	tileID := d.tileID
	version := d.snapshotTile.Version
	req := &rpc.CloneTileRequest{
		Path:       rpc.Path{WellIDs: srcPath},
		TileID:     tileID,
		Version:    version,
		DestGridID: dstGridID,
		DestPath:   rpc.Path{WellIDs: dstPath},
		X:          dropX,
		Y:          dropY,
	}
	a.postCrossGridMutate("CloneTile", srcGridID, dstGridID, func(ctx context.Context) (*rpc.Tile, error) {
		return a.cl.CloneTile(ctx, req)
	}, d)
}

// runDeleteTile fires DeleteTile against the dragged source tile. Used
// when the left-button-move gesture drops onto a black-hole sink.
func (a *App) runDeleteTile(d *dragState, t *dropTarget) {
	var dstGridID int64
	if t != nil {
		dstGridID = t.gridID
	}
	req := &rpc.DeleteTileRequest{
		Path:    rpc.Path{WellIDs: slices.Clone(d.srcPath)},
		TileID:  d.tileID,
		Version: d.snapshotTile.Version,
	}
	// Drop any cached liveness probe for this tile — the row is
	// about to vanish and so will the tmux session the server side
	// kills behind it.
	delete(a.shellAlive, d.tileID)
	delete(a.shellAliveProbing, d.tileID)
	a.postTwoGridMutate("DeleteTile", d.srcGridID, dstGridID, func(ctx context.Context) error {
		return a.cl.DeleteTile(ctx, req)
	})
}

// tileAtCellInTarget returns the tile the cursor is *inside* of in
// the resolved drop target's grid (which may be a well's child grid),
// or nil. Hit-test semantics: use floor, never round — round-half
// silently misses the lower-right portion of every cell, which broke
// the black-hole delete trigger across half each black hole.
func (a *App) tileAtCellInTarget(t *dropTarget, sx, sy float64) *rpc.Tile {
	cellX, cellY := dragdrop.FloorCellAt(t.originX, t.originY, t.cellSize, sx, sy)
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
	gid := a.gridIDForPath(p.Path)
	req := &rpc.ResizeTileRequest{
		Path:    rpc.Path{WellIDs: slices.Clone(p.Path)},
		TileID:  n.ID,
		Version: n.Version,
		X:       rd.tileNewX,
		Y:       rd.tileNewY,
		W:       rd.tileNewW,
		H:       rd.tileNewH,
	}
	a.postTileMutate("ResizeTile", gid, func(ctx context.Context) (*rpc.Tile, error) {
		return a.cl.ResizeTile(ctx, req)
	}, nil)
}

// commitResize applies the final ratio and, if either side is below
// rightCloseThreshold, collapses that side.
func (a *App) commitResize(rd *rightDragState, sx, sy float64) {
	ratio, collapse := gesture.ResizeOutcome(rd.container, rd.splitDir, sx, sy, rightCloseThreshold)
	rd.targetSplit.Ratio = ratio
	// Before a side is dropped, flush every leaf pane it holds: persist
	// unsaved text edits and freeze any live URL/shell stream. Otherwise a
	// collapsed pane's live view parks hidden (still running, never frozen)
	// and recent edits are lost — the dropped pane never hit the ascent
	// save path.
	switch collapse {
	case gesture.CollapseA:
		a.flushDroppedSubtree(rd.targetSplit.A)
		_ = a.tree.CollapseSplit(rd.targetSplit, true)
	case gesture.CollapseB:
		a.flushDroppedSubtree(rd.targetSplit.B)
		_ = a.tree.CollapseSplit(rd.targetSplit, false)
	}
}

// flushDroppedSubtree saves/freezes every leaf pane in a subtree that's
// about to be removed by a split collapse.
func (a *App) flushDroppedSubtree(n pane.TreeNode) {
	pane.WalkLeaves(n, func(p *pane.Pane) {
		a.flushPaneBeforeDrop(p)
	})
}

// flushPaneBeforeDrop persists a single pane's descended state before the
// pane disappears: text edits + framed window for a text descent, and a
// freeze for a live URL or shell stream. Each step is a no-op when it
// doesn't apply (kind guard inside saveFileBeforeAscent; the close* helpers
// no-op when the pane has no live stream).
func (a *App) flushPaneBeforeDrop(p *pane.Pane) {
	if p.TextFocus != 0 {
		if g, ok := a.c.Grid(a.gridIDForPath(p.Path)); ok {
			if file, ok := g.Tiles[p.TextFocus]; ok {
				a.saveFileBeforeAscent(p, file)
			}
		}
	}
	a.closeURLStream(p.ID)
	a.closeShellStream(p.ID)
}

// leftResizeState carries the in-flight left-button pane-boundary resize.
// Mirrors the resize-only fields of rightDragState; the left button keeps
// its own state so the right-button routing (which keys off button 2)
// stays untouched.
type leftResizeState struct {
	targetSplit *pane.Split
	splitDir    pane.Direction
	container   pane.Rect
}

// armLeftResize starts a left-button boundary resize if (sx, sy) sits in a
// resize band of pane p that has a divider on that side. Returns true if a
// resize was armed (caller should stop interpreting the click).
func (a *App) armLeftResize(p *pane.Pane, r pane.Rect, sx, sy float64) bool {
	// The corner circle (bottom-right) can overlap a resize band when the
	// pane has a sibling; its left-click action always wins.
	if pointInPlus(r, sx, sy) {
		return false
	}
	region := pane.ClassifyRegion(r, resizeBandPx, sx, sy)
	if !region.IsResize() {
		return false
	}
	d := a.dividerOnSide(p, region.Side())
	if d == nil {
		return false
	}
	a.leftResize = &leftResizeState{
		targetSplit: d.Split,
		splitDir:    d.Dir,
		container:   pane.Rect{X: d.ContainerRect.X, Y: d.ContainerRect.Y, W: d.ContainerRect.W, H: d.ContainerRect.H},
	}
	return true
}

// onLeftResizeMove applies the live divider ratio for the in-flight
// left-button resize, clamped so neither side shrinks past leftResizeMinPx
// — the left button never closes a pane.
func (a *App) onLeftResizeMove(sx, sy float64) {
	lr := a.leftResize
	if lr == nil {
		return
	}
	ratio := pane.RatioFromCursor(lr.container, lr.splitDir, sx, sy)
	lr.targetSplit.Ratio = pane.ClampRatioToMinPx(lr.container, lr.splitDir, ratio, leftResizeMinPx)
	a.draw()
}

// leftResizeMinPx is the smallest a pane side may shrink to under a
// left-drag resize. Unlike the right-button resize (which collapses a side
// below rightCloseThreshold), the left button clamps here so a minimized
// pane is always recoverable. Passed into pane.ClampRatioToMinPx.
const leftResizeMinPx = 32.0

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
	ratio, ok := gesture.SplitOutcome(rd.splitSide, rd.splitPane, resizeBandPx, rd.startX, rd.startY, sx, sy)
	if !ok {
		return
	}
	p := a.tree.FindPane(rd.splitPaneID)
	if p == nil {
		return
	}
	_ = a.tree.SetFocus(p.ID)
	np, err := a.tree.SplitOnSideAt(rd.splitSide, ratio)
	if err != nil {
		return
	}
	// A new pane is a clone of the source, so without this it would just
	// duplicate the current view. Auto-ascend it one level: split off a
	// URL/text descent and the new pane shows the parent grid (not a
	// second copy of the page); split off a child grid and it shows the
	// grid above. At root there's nowhere to ascend, so it stays put.
	if np != nil && a.canAscend(np) {
		a.ascendPane(np)
	}
}

// commitURLRefresh commits the URL refresh gesture if the cursor was
// dragged past urlRefreshThresholdPx downward from the right-down origin.
// On commit, the live URL stream is opened for the descended tile.
// Releasing before the threshold (or dragging back above origin) is a
// silent cancel.
func (a *App) commitURLRefresh(rd *rightDragState, sx, sy float64) {
	_ = sx
	if !gesture.URLRefreshArmed(rd.startY, sy, urlRefreshThresholdPx) {
		// Threshold not reached — cancel silently.
		return
	}
	p := a.tree.FindPane(rd.refreshPaneID)
	if p == nil || p.TextFocus == 0 {
		return
	}
	a.openURLStream(p, rd.refreshTileID)
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
			if d.Dir == pane.Horizontal && dragdrop.NearPx(d.Rect.Y+d.Rect.H/2, r.Y) {
				return &divs[i]
			}
		case pane.SideBottom:
			if d.Dir == pane.Horizontal && dragdrop.NearPx(d.Rect.Y+d.Rect.H/2, r.Y+r.H) {
				return &divs[i]
			}
		case pane.SideLeft:
			if d.Dir == pane.Vertical && dragdrop.NearPx(d.Rect.X+d.Rect.W/2, r.X) {
				return &divs[i]
			}
		case pane.SideRight:
			if d.Dir == pane.Vertical && dragdrop.NearPx(d.Rect.X+d.Rect.W/2, r.X+r.W) {
				return &divs[i]
			}
		}
	}
	return nil
}

// paneToDragdrop builds a dragdrop.Pane (screen rect + viewport) for
// pane p drawn into screen rect r. Centralized so callers don't all
// reach into the same five fields by hand.
func paneToDragdrop(p *pane.Pane, r pane.Rect) dragdrop.Pane {
	return dragdrop.Pane{
		ScreenX: r.X, ScreenY: r.Y, ScreenW: r.W, ScreenH: r.H,
		Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom, CellPx: cellPx,
	}
}
