//go:build js && wasm

package main

import (
	"context"
	"math"
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/dragdrop"
	"github.com/josephburnett/gridwell/client/gesture"
	"github.com/josephburnett/gridwell/client/pane"
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

// resizeBandPx is the thickness of the resize zone near each pane edge: the
// band where a drag grabs the divider. The interaction model is region-based
// throughout.
const resizeBandPx = 10.0

// rightDragKind classifies an in-flight right-button gesture. Set on
// mousedown; the tile-vs-pane fork is decided by where the cursor
// landed (over a tile → tile gesture; otherwise → pane gesture). Never
// changes mid-gesture.
type rightDragKind int

const (
	rightDragNone rightDragKind = iota
	rightDragSwap
	rightDragSplit
	// rightDragTileCenter is armed when right-down lands in the inner
	// 1/3 × 1/3 of a tile (cell coords). It's the copy/link grab handle:
	// dragging past the threshold materializes a ghost via the standard
	// a.dragging machinery — a solid copy, or a dashed link when ctrl was
	// held at the press; bare release is a no-op.
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

	// Split-only. The axis is fixed by the grabbed border; the side, and
	// the host pane, follow the drag and are resolved at release. Either
	// side of a border behaves identically, and the direction can flip
	// mid-gesture.
	splitAxis pane.Direction

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
}

// rightDragIntent reads the right-button press's modifier and returns what a
// drag off it means: ctrl flips the right button from copy to link. It is the
// one place that reading lives, so the canvas, the text overlay, and the
// shell overlay cannot disagree about which modifier means link — and the
// intent is fixed HERE, at the press, like every other gesture fact
// (gesture.Kind), so the ghost previews the same outcome the release commits
// even if the key is let go mid-drag.
func rightDragIntent(ev js.Value) dragdrop.Intent {
	if ev.Truthy() && ev.Get("ctrlKey").Truthy() {
		return dragdrop.IntentLink
	}
	return dragdrop.IntentCopy
}

// onRightDown classifies the right-down and arms the matching gesture
// state. Tile gestures (cap/redig/fill on bare release; resize on
// drag) take priority when the cursor is over a tile in a grid view.
// No tree or store edits happen here — those wait for release (or the
// next move tick, for pane resize).
//
// intent is what a drag off this press will leave at the destination
// (rightDragIntent): copy, or link when ctrl was held. It reaches only the
// tile-center arm; the pane gestures — swap, split, tile resize — have no
// destination and ignore it.
//
// Caller has already verified there's no animation in flight and
// (sx, sy) is over pane p with screen rect r.
func (a *App) onRightDown(p *pane.Pane, r pane.Rect, sx, sy float64, intent dragdrop.Intent) {
	// Resolve the facts gesture.Classify orders, holding onto the lookups
	// (tile, divider, region) so the arming switch reuses them instead of
	// recomputing. Every lookup here is a pure read; the state edits happen
	// only in the arming switch below.
	in := gesture.Input{
		InGridView: p.ContentID() == "",
		Region:     pane.ClassifyRegion(r, resizeBandPx, sx, sy),
	}

	var tile *rpc.Tile
	if in.InGridView {
		tile = a.tileAtScreen(p, r, sx, sy)
		in.OverTile = tile != nil
		if tile != nil {
			in.InTileCenter = inTileCenter(tile, p, r, sx, sy)
		}
	}

	switch gesture.Classify(in) {
	case gesture.TileCenter, gesture.TileResize:
		// armTileGesture re-derives center-vs-resize via dragdrop and arms
		// the matching state (and primes the copy-or-link ghost for the
		// center).
		a.armTileGesture(p, r, tile, sx, sy, intent)
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
			kind:      rightDragSplit,
			startX:    sx,
			startY:    sy,
			curX:      sx,
			curY:      sy,
			splitAxis: in.Region.Side().Direction(),
		}
	}
}

// forwardedPaneAt resolves a press forwarded from a live URL view's preload
// to the pane under it, refusing while a viewport transition animates — the
// same two guards the canvas's own onMouseDown opens with, shared by the
// three forwarded handlers so none of them can forget the transition gate.
func (a *App) forwardedPaneAt(sx, sy float64) (*pane.Pane, pane.Rect, bool) {
	if a.trans.Any() {
		return nil, pane.Rect{}, false
	}
	return a.paneAtScreen(sx, sy)
}

// onForwardedRightDown begins a right-button pane gesture at canvas
// coordinates (sx, sy) that originated over a live URL view. The native
// WebContentsView swallows the renderer's own mouse events, so its injected
// preload forwards the right-button press here (via main) — mirroring how the
// shell's in-renderer xterm overlay forwards its right button. We run the same
// onRightDown the canvas uses, then draw(): the gesture is now in flight, so
// syncURLViews parks the view (liveOverlaysHidden), exposing the canvas so the
// rest of the drag (move/up) lands on it natively. This is the right-button
// half of onMouseDown, minus the DOM event (the preload already preventDefaulted).
func (a *App) onForwardedRightDown(sx, sy float64) {
	p, r, ok := a.forwardedPaneAt(sx, sy)
	if !ok {
		return
	}
	// focusToPane closes the menu on the de-focused pane, through
	// menu.TransferFocus, and refreshes the text overlay: the same focus
	// semantics as the canvas path. Omitting it here would strand the menu
	// on the old pane after a right-drag over a live URL view moved focus.
	a.focusToPane(p)
	// IntentCopy, not a forwarded modifier: this press came from a live URL
	// view, so the pane is in a content descent and gesture.Classify can only
	// reach the pane gestures, which ignore the intent. There is no tile
	// under a live view to copy or link.
	a.onRightDown(p, r, sx, sy, dragdrop.IntentCopy)
	a.draw()
}

// onForwardedMiddleDown ascends the pane at canvas coordinates (sx, sy) when a
// middle-button press originated over a live URL view. Like the right button,
// the native WebContentsView swallows the renderer's own middle clicks, so its
// preload forwards the press here (via main). Middle-click is the universal
// ascend gesture; this is its live-URL path, mirroring onForwardedRightDown.
func (a *App) onForwardedMiddleDown(sx, sy float64) {
	p, _, ok := a.forwardedPaneAt(sx, sy)
	if !ok {
		return
	}
	a.menu.Close()
	a.ascendPane(p)
}

// onForwardedLeftDown handles a left-button press that originated over a live
// URL view. The native WebContentsView swallows the canvas's own mousedown, so
// the view's preload forwards the press here (via main) in canvas coords —
// without preventing the default, so in-page interaction, selection, and link
// clicks still reach the page. Two intents are managed here:
//   - pane focus (and the menu's focused-pane invariant), via focusToPane;
//   - a boundary resize, when the press lands in a divider's grab band. The
//     band straddles the divider, so its inner half sits on the live view.
//     Mirroring the right-button twin (onForwardedRightDown), arming parks
//     the view (armLeftResize draws) so the rest of the drag lands on the
//     canvas. Without this, a left border-drag that grabbed the live-view
//     half of the band could never start.
func (a *App) onForwardedLeftDown(sx, sy float64) {
	p, r, ok := a.forwardedPaneAt(sx, sy)
	if !ok {
		return
	}
	a.focusToPane(p)
	a.armLeftResize(r, sx, sy)
}

// onForwardedContextMenu handles a live URL view's native context menu opening
// on pane paneID. The view swallows a plain right-press — its preload only
// forwards one once it has become a drag — so the menu is the whole gesture the
// renderer ever hears about, and without this a right-click and a Reload acted
// on a pane that never took focus. Right-clicking a pane is interacting with
// it, so it moves focus, through the same focusToPane every other press path
// uses; the rest of the menu stays main's business.
//
// Keyed by pane id, not by cursor position: main knows which view was
// right-clicked, and the bar-circle door has no cursor over the pane at all. A
// pane that is not in the live tree (a parked level) is not focusable, and is
// skipped.
func (a *App) onForwardedContextMenu(paneID string) {
	p := a.tree.FindPane(paneID)
	if p == nil {
		return
	}
	a.focusToPane(p)
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
func (a *App) tileAtScreen(p *pane.Pane, r pane.Rect, sx, sy float64) *rpc.Tile {
	cellX, cellY := cellAtScreen(p, r, sx, sy)
	return a.tileAtCell(p, cellX, cellY)
}

// armTileGesture installs the right state for a right-button-down on
// a tile. The model is the same for every tile kind (well, text, URL,
// shell, pane):
//   - Center 1/3 × 1/3: rightDragTileCenter — drag-past-threshold
//     clones the tile, or links it when intent is IntentLink (via the
//     standard a.dragging machinery, armed in parallel); bare release
//     does nothing.
//   - Outside center: rightDragTileResize — rubber-band the footprint.
//     A resize has no destination, so it ignores the intent: ctrl on the
//     resize ring changes nothing.
//
// In both cases drawRightDragPreview paints the 5-zone hotspot
// overlay so the user can see all the affordances on the tile at a
// glance.
func (a *App) armTileGesture(p *pane.Pane, r pane.Rect, n *rpc.Tile, sx, sy float64, intent dragdrop.Intent) {
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
		a.armRightClone(p, r, n, sx, sy, intent)
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
	case rightDragSwap:
		a.commitSwap(rd, sx, sy)
	case rightDragSplit:
		a.commitSplit(rd, sx, sy)
	case rightDragTileCenter:
		a.commitTileCenter(sx, sy)
	case rightDragTileResize:
		a.commitTileResize(rd)
	}
	a.draw()
	a.scheduleURLUpdate()
}

// advanceCloneDrag drives the a.dragging ghost from a right-button move: the
// one threshold-and-ghost promotion the left-drag path runs
// (advanceDragGhost), then cursor tracking. Below the threshold the cursor
// still tracks, so the gesture starts from where the button actually is.
func (a *App) advanceCloneDrag(sx, sy float64) {
	d := a.dragging
	if d == nil {
		return
	}
	if !a.advanceDragGhost(d, sx, sy) {
		d.curScreenX = sx
		d.curScreenY = sy
		return
	}
	if a.ghost != nil {
		// Same DecideDrop verdict the right-drag commit (commitRightClone)
		// uses, off the same d.intent. Preview and commit can't diverge.
		a.previewDrop(d, sx, sy)
	}
	d.curScreenX = sx
	d.curScreenY = sy
}

// armRightClone primes the a.dragging state so a drag from the center zone of
// tile n will clone it, or link it when ctrl was held at the press
// (IntentLink). The ghost itself is materialized only once the cursor moves
// past dragThreshold — same convention as left-click drags — so a bare
// right-click leaves the world unchanged.
//
// We don't hide the original tile — both intents create, and the source stays
// — and the cursor offset inside the tile is preserved so the grab point
// tracks the cursor.
func (a *App) armRightClone(p *pane.Pane, r pane.Rect, n *rpc.Tile, sx, sy float64, intent dragdrop.Intent) {
	ps := paneToDragdrop(p, r)
	cxF, cyF := ps.ScreenToCell(sx, sy)
	tlX, tlY := ps.CellToScreen(float64(n.X), float64(n.Y))
	a.dragging = &dragState{
		originPaneID:  p.ID,
		originFocused: true, // right-down focused the pane before arming
		intent:        intent,
		startScreenX:  sx,
		startScreenY:  sy,
		curScreenX:    sx,
		curScreenY:    sy,
		srcGridID:     a.gridIDForPane(p),
		srcCellSize:   cellPx * p.Zoom,
	}
	a.dragging.grabTile(n, cxF, cyF, tlX, tlY)
}

// commitTileCenter handles release of a center-zone gesture. With the
// unified mouse-only model the bare-release semantics are: no-op. The
// center is "the copy/link grab handle" — the drop happens by dragging
// past the threshold. If a.dragging was promoted to "started" by motion,
// commit it through commitRightClone, which reads the armed intent;
// otherwise just clear the priming state so subsequent clicks aren't
// affected.
func (a *App) commitTileCenter(sx, sy float64) {
	d := a.dragging
	a.dragging = nil
	if d == nil || !d.started {
		a.ghost = nil
		a.draw()
		return
	}
	a.commitRightClone(d, sx, sy)
}

// commitRightClone resolves the drop target at (sx, sy) and either deletes —
// a drop on the source pane's + button, shown as a trashcan — fires CloneTile
// or the link create, or snaps the ghost back on a rejected drop. The async
// RPC fires in a goroutine and the local cache is patched by the event when it
// lands. Dropping on the trashcan deletes whichever button armed the drag;
// otherwise a right-drag copies, and a ctrl + right-drag links.
func (a *App) commitRightClone(d *dragState, sx, sy float64) {
	// The same snapshot-then-DecideDrop discipline as the left-drag commit
	// (onMouseUp), through the one gatherer (dropInputAt), so the preview
	// (advanceCloneDrag) and the commit share one decision off d.intent. A
	// plain right-drag clones everywhere — a copy of the dragged tile, and a
	// link tile copies as another link.
	in, t, dropX, dropY := a.dropInputAt(d, sx, sy, true /* placement */)

	switch dragdrop.DecideDrop(in) {
	case dragdrop.DropDelete:
		// Trashcan delete: dropping on the source pane's + button, shown as
		// a trashcan during the drag, deletes the grabbed tile whichever
		// button armed it — the same gesture and outcome as the left-drag
		// delete path, and what advanceCloneDrag previewed.
		a.runDeleteTile(d, nil)
		a.ghost = nil
		a.draw()
		return
	case dragdrop.DropRejected:
		// No target, the same cell, or an occupied one: snap back.
		a.cancelDragSnapBack(d)
		return
	case dragdrop.DropLink:
		// Ctrl was held at the press: the destination gains a reference and
		// the source stays put, whatever namespace the drop landed in. It is
		// the one link commit the cross-namespace left-drag uses, so the two
		// gestures cannot make two different kinds of link. Nothing to
		// unhide: a creating drag never hid the source.
		a.landGhostAtCell(t, dropX, dropY)
		a.commitLinkDrop(d, t, dropX, dropY)
		a.draw()
		return
	}

	// DropClone.
	a.landGhost(t.pane.ID, t.cellSize, t.originX+float64(dropX)*t.cellSize, t.originY+float64(dropY)*t.cellSize)
	dstGridID := t.gridID
	srcGridID := d.srcGridID
	tileID := d.tileID
	req := &rpc.CloneTileRequest{
		TileID:     tileID,
		DestGridID: dstGridID,
		X:          dropX,
		Y:          dropY,
	}
	a.post(write{
		label: "CloneTile", gid: srcGridID, alsoGID: dstGridID, refetchOnOK: true,
		call: func(ctx context.Context) error {
			_, err := a.cl.CloneTile(ctx, req)
			return err
		},
		undo: func() { a.snapBackToOrigin(d) },
	})
}

// runDeleteTile fires DeleteTile against the dragged source tile. Used when
// a move or clone drag drops onto the source pane's + (trashcan) button. The
// dropTarget is optional (nil when deleting via the button) — it only refines
// which destination grid's cache to refresh.
func (a *App) runDeleteTile(d *dragState, t *dropTarget) {
	var dstGridID string
	if t != nil {
		dstGridID = t.gridID
	}
	req := &rpc.DeleteTileRequest{TileID: d.tileID}
	// Drop any cached liveness probe for this tile — the row is
	// about to vanish and so will the tmux session the server side
	// kills behind it.
	delete(a.shellAlive, d.tileID)
	delete(a.shellAliveProbing, d.tileID)
	// No snapback and no parked value: the tile is going to vanish either
	// way, so there is no ghost to roll back. Both grids refetch whatever
	// the server said, and a failed delete putting the row back on screen is
	// the reconcile.
	src, dst := d.srcGridID, dstGridID
	refetch := func() {
		a.fetchGrid(src)
		if dst != "" && dst != src {
			a.fetchGrid(dst)
		}
	}
	a.post(write{
		label: "DeleteTile", gid: src, alsoGID: dst, refetchOnOK: true,
		call: func(ctx context.Context) error { return a.cl.DeleteTile(ctx, req) },
		undo: refetch,
	})
}

// commitTileResize commits the proposed (X, Y, W, H) via PlaceTile — the one
// placement writeback (a resize is a placement write within the same grid).
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
	gid := a.gridIDForPane(p)
	req := &rpc.PlaceTileRequest{
		TileID: n.ID,
		GridID: n.GridID,
		X:      rd.tileNewX,
		Y:      rd.tileNewY,
		W:      rd.tileNewW,
		H:      rd.tileNewH,
	}
	a.postTileMutate("PlaceTile", gid, func(ctx context.Context) (*rpc.Tile, error) {
		return a.cl.PlaceTile(ctx, req)
	}, nil)
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
// doesn't apply (kind guard inside saveTextBeforeAscent; the close* helpers
// no-op when the pane has no live stream).
func (a *App) flushPaneBeforeDrop(p *pane.Pane) {
	if p.ContentID() != "" {
		if g, ok := a.c.Grid(a.gridIDForPane(p)); ok {
			if file, ok := g.Tiles[p.ContentID()]; ok {
				a.saveTextBeforeAscent(p, file)
			}
		}
	}
	// The dropped pane never reaches the ascent save path or the settle
	// persister again, since it leaves the tree now, so flush its grid
	// framing here.
	a.persistPaneFraming(p)
	// The pane is vanishing: freeze + close its live sessions and drop all its
	// per-pane state atomically (forgetPane closes the streams), so nothing is
	// left orphaned behind a now-dead pane id.
	a.forgetPane(p.ID)
}

// leftResizeAxis is one armed divider inside a left-button resize: the split
// being dragged plus the collapse facts its release decides from.
type leftResizeAxis struct {
	targetSplit *pane.Split
	splitDir    pane.Direction
	// crush is the close-threshold plan: the cursor positions past which
	// each corridor segment outward from the boundary is pressed to close —
	// the per-segment steps of the corridor wall. The red preview and the
	// release both read crush.Red(), so there is one verdict.
	crush pane.CrushPlan
}

// leftResizeState carries the in-flight left-button pane-boundary resize. The
// left button keeps its own state so the right-button routing, which keys off
// button 2, stays untouched. The left drag owns closing too, so crushing a
// side past the wall and releasing collapses that side.
//
// A press grabs at most one divider per axis (pane.GrabDividers): on a
// divider's length it grabs one, at a T-intersection it grabs both, and then
// axes holds two entries that this one gesture drives together. There is no
// separate corner gesture — a corner is two ordinary resizes sharing a press,
// so the minimum clamp, the cascade, and the crush verdict are the same code
// on each axis.
type leftResizeState struct {
	axes []leftResizeAxis
	// curX/curY is the last cursor the move applied. The preview and the
	// release read the same point, so the red warning cannot mark a
	// different side than the release collapses. It is initialized to the
	// arm point: left at zero, a bare click's verdict would read cursor
	// (0,0), always past the wall, and close a pane in one click. There is
	// deliberately no captured container rect — the cascade moves ancestor
	// ratios mid-drag, and a stale copy of the split's geometry closes panes
	// on a legal mid-corridor release. Everything derives live from the tree
	// (pane.CorridorWalls, pane.LocateSplit).
	curX, curY float64
}

// armLeftResize starts a left-button boundary resize if (sx, sy) sits in the
// grab band of a pane edge that has a divider on it. One armed resize per axis
// grabbed, so a press at the corner of three panes drives both. Returns true if
// anything was armed (caller should stop interpreting the click).
func (a *App) armLeftResize(r pane.Rect, sx, sy float64) bool {
	grab, divs := a.dividerGrab(r, sx, sy)
	// gesture.ResizeAffordance owns the gating, and dividerResizeCursor shares
	// it so the hover cursor cannot disagree with where a drag arms.
	arm, _ := gesture.ResizeAffordance(grab)
	if !arm {
		return false
	}
	var axes []leftResizeAxis
	add := func(d pane.Divider) {
		crush, ok := pane.PlanCrush(a.tree.Root, a.rootLayoutRect(), d.Split, pane.MinPanePx)
		if !ok {
			return
		}
		axes = append(axes, leftResizeAxis{targetSplit: d.Split, splitDir: d.Dir, crush: crush})
	}
	if grab.HasHoriz {
		add(divs[grab.Horiz])
	}
	if grab.HasVert {
		add(divs[grab.Vert])
	}
	if len(axes) == 0 {
		return false
	}
	a.leftResize = &leftResizeState{axes: axes, curX: sx, curY: sy}
	// Park live overlays now; liveOverlaysHidden consults leftResize. The
	// grab band straddles the divider, so half of it can sit over a live
	// WebContentsView that would otherwise eat the next mousemove and kill
	// the drag.
	a.draw()
	return true
}

// dividerResizeCursor returns the CSS cursor to show while hovering a
// grabbable pane split divider at (sx, sy): "ew-resize" over a vertical
// divider, "ns-resize" over a horizontal one, a diagonal at a corner that
// grabs both, or "" when the point isn't in any divider's grab band. It
// mirrors armLeftResize exactly — same pane.GrabDividers verdict through the
// same gesture.ResizeAffordance — so the cursor appears precisely where a
// left-drag would resize, and says how many axes it would move. The grab band
// is resizeBandPx (10) wide even though the divider line is drawn at 1px — the
// cursor is what makes that otherwise-invisible band discoverable.
func (a *App) dividerResizeCursor(sx, sy float64) string {
	_, r, ok := a.paneAtScreen(sx, sy)
	if !ok {
		return ""
	}
	grab, _ := a.dividerGrab(r, sx, sy)
	_, cursor := gesture.ResizeAffordance(grab)
	return cursor
}

// onLeftResizeMove applies the live divider move for the in-flight
// left-button resize, one armed axis at a time — a corner grab moves both from
// the one cursor, each reading only its own axis's coordinate. The cascade
// (pane.ResizeThrough) compresses the pane adjacent to the divider to its
// minimum first, then the next along the axis, across same-axis splits, walled
// by the sum of minimums, so the move itself never closes a pane.
func (a *App) onLeftResizeMove(sx, sy float64) {
	lr := a.leftResize
	if lr == nil {
		return
	}
	lr.curX, lr.curY = sx, sy
	// Each axis re-reads the tree, so the second one sees the first one's
	// ratios: the geometry is live, never a copy taken at arm.
	for i := range lr.axes {
		ax := &lr.axes[i]
		cursor := sx
		if ax.splitDir == pane.Horizontal {
			cursor = sy
		}
		// Fold the move into the red state before the layout follows the
		// cursor: the pre-move layout is what tells a deeper press, which
		// stays red, from a back-off, which clears, while a crushed pane
		// rides the drag at its minimum.
		ax.crush.Update(a.tree.Root, a.rootLayoutRect(), ax.targetSplit, pane.MinPanePx, cursor)
		// Walled at the universal pane minimum: the drag itself never
		// collapses. The adjacent pane visibly crushes toward the wall,
		// signaling that a release now closes it; the release decides.
		pane.ResizeThrough(a.tree.Root, a.rootLayoutRect(), ax.targetSplit, cursor, pane.MinPanePx)
	}
	a.draw()
}

// finishLeftResize is the left release. The live layout was already applied
// during the move, only to the grabbed borders, so the release only closes
// what the drag pressed: every corridor segment red in the preview is flushed
// and removed, adjacent first, on each armed axis. The release reads the same
// stored red state the last move computed and the preview drew, so the verdict
// is identical to the warning the user saw.
//
// A corner grab can red a segment on one axis that lies inside a segment the
// other axis closes, so a segment already gone from the tree is skipped rather
// than flushed twice.
func (a *App) finishLeftResize() {
	lr := a.leftResize
	a.leftResize = nil
	if lr == nil {
		return
	}
	for i := range lr.axes {
		for _, seg := range lr.axes[i].crush.Red() {
			if !pane.HasSegment(a.tree.Root, seg) {
				continue
			}
			a.flushDroppedSubtree(seg)
			if !a.tree.RemoveSegment(seg) {
				break
			}
		}
	}
	a.draw()
	a.scheduleURLUpdate()
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

// commitSplit converts the in-flight preview into a real split. The side and
// the host pane follow the drag: the new pane opens on whichever side of the
// border the cursor traveled to, in the pane the cursor is in at release,
// between the grabbed border and the cursor. A sub-threshold drag, an
// off-pane release, or a position that cannot leave both children
// pane.MinPanePx cancels silently.
func (a *App) commitSplit(rd *rightDragState, sx, sy float64) {
	side, active := gesture.SplitSideFromDrag(rd.splitAxis, rd.startX, rd.startY, sx, sy)
	if !active {
		return
	}
	p, r, ok := a.paneAtScreen(sx, sy)
	if !ok {
		return
	}
	ratio, ok := gesture.SplitOutcome(side, r, sx, sy)
	if !ok {
		return
	}
	_ = a.tree.SetFocus(p.ID)
	np, err := a.tree.SplitOnSideAt(side, ratio)
	if err != nil {
		return
	}
	// A new pane is a clone of the source, deliberately: a split means
	// another view of where I am, so a grid view clones verbatim, with the
	// same place stack. The one exception is a content frame — a live URL or
	// shell view cannot be duplicated, since there is one native view or PTY
	// attachment per tile and pane — so the new pane ascends just the
	// content level and shows the grid containing the tile.
	if np != nil && np.ContentID() != "" {
		a.ascend(np, 1, true)
	}
}

// dividerGrab asks which dividers a press at (sx, sy) in pane p grabs — one
// per axis, both at a T-intersection. The decision is pane.GrabDividers; this
// only supplies the geometry, and returns the divider slice so the caller can
// resolve the grab's indices.
// r is the pane's own laid-out rect, as the caller got it from paneAtScreen.
// One owner for the layout rect: pane rects and divider geometry both come
// from layoutPanes over rootLayoutRect. A second copy here — the full window —
// would drift by the bar's height and the adjacency match inside GrabDividers
// would never fire.
func (a *App) dividerGrab(r pane.Rect, sx, sy float64) (pane.DividerGrab, []pane.Divider) {
	divs := pane.Dividers(a.tree, a.rootLayoutRect(), resizeBandPx)
	return pane.GrabDividers(divs, r, resizeBandPx, sx, sy), divs
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
