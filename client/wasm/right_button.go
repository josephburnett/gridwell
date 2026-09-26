//go:build js && wasm

package main

import (
	"context"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"math"
	"syscall/js"

	"github.com/josephburnett/gridwell/client/dragdrop"
	"github.com/josephburnett/gridwell/client/gesture"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/traceevent"
)

// resizeBandPx is the band near each pane edge where a drag grabs a divider.
const resizeBandPx = 10.0

// rightDragKind is decided at mousedown and never changes mid-gesture.
type rightDragKind int

const (
	rightDragNone rightDragKind = iota
	rightDragSwap
	rightDragSplit
	// rightDragTileCenter is armed on a right-down in the inner 1/3 by 1/3 of a
	// tile: the copy/link grab handle, where a bare release is a no-op.
	rightDragTileCenter
	// rightDragTileResize is armed outside that center. The pin is the corner
	// diagonally opposite the click quadrant; the cursor may cross it.
	rightDragTileResize
)

type rightDragState struct {
	kind           rightDragKind
	startX, startY float64
	curX, curY     float64

	// Swap-only.
	originPaneID string

	// Split-only. The axis is fixed by the grabbed border; the side and the host
	// pane follow the drag, so the direction can flip until release.
	splitAxis pane.Direction

	// Tile-only.
	tilePaneID string
	tileNode   *gridwellv1.Tile
	tilePane   *pane.Pane // for path/grid lookups at commit time
	tilePaneR  pane.Rect  // pane rect at right-down (for cell mapping)

	// rightDragTileCenter-only. cursorInCenter lets the preview disable when
	// the cursor leaves the center zone and re-engage when it comes back.
	cursorInCenter bool

	// rightDragTileResize-only. The moving corner starts at the tile's other
	// diagonal corner and translates by (cursor cell - click cell), so a
	// right-down with no movement leaves the tile unchanged.
	pinX, pinY               int64
	origMovingX, origMovingY int64
	clickCellX, clickCellY   int64
	tileNewX, tileNewY       int64
	tileNewW, tileNewH       int64
}

// rightDragIntent reads the press's modifier: ctrl flips the right button from
// copy to link. It is the one place that reading lives, and the intent is fixed
// at the press, so letting the key go mid-drag changes nothing.
func rightDragIntent(ev js.Value) dragdrop.Intent {
	if ev.Truthy() && ev.Get("ctrlKey").Truthy() {
		return dragdrop.IntentLink
	}
	return dragdrop.IntentCopy
}

// onRightDown arms the matching gesture state. No tree or store edits happen
// here; those wait for release, or the next move tick for a pane resize. intent
// reaches only the tile-center arm: the pane gestures have no destination.
func (a *App) onRightDown(p *pane.Pane, r pane.Rect, sx, sy float64, intent dragdrop.Intent) {
	// Pure reads, held so the arming switch below reuses them.
	in := gesture.Input{
		InGridView: p.ContentID() == "",
		Region:     pane.ClassifyRegion(r, resizeBandPx, sx, sy),
	}

	var tile *gridwellv1.Tile
	if in.InGridView {
		tile = a.tileAtScreen(p, r, sx, sy)
		in.OverTile = tile != nil
		if tile != nil {
			in.InTileCenter = inTileCenter(tile, p, r, sx, sy)
		}
	}

	switch gesture.Classify(in) {
	case gesture.TileCenter, gesture.TileResize:
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

// forwardedPaneAt refuses while a viewport transition animates. All three
// forwarded handlers share it, so none can forget the transition gate.
func (a *App) forwardedPaneAt(sx, sy float64) (*pane.Pane, pane.Rect, bool) {
	if a.trans.Any() {
		return nil, pane.Rect{}, false
	}
	return a.paneAtScreen(sx, sy)
}

// onForwardedRightDown begins a right-button pane gesture that started over a
// live URL view, whose native WebContentsView swallows the renderer's own mouse
// events. The draw() parks the view, so the rest of the drag lands on canvas.
func (a *App) onForwardedRightDown(sx, sy float64) {
	a.emit(traceevent.Press(a.paneIDAt(sx, sy), 2, traceevent.Mods{}, true))
	p, r, ok := a.forwardedPaneAt(sx, sy)
	if !ok {
		return
	}
	// Without this a right-drag over a live URL view strands the menu.
	a.focusToPane(p)
	// IntentCopy: a content descent can only arm pane gestures, which ignore it.
	a.onRightDown(p, r, sx, sy, dragdrop.IntentCopy)
	a.draw()
}

// onForwardedMiddleDown is middle-click ascent's live-URL path, because the
// WebContentsView swallows the renderer's own middle clicks.
func (a *App) onForwardedMiddleDown(sx, sy float64) {
	a.emit(traceevent.Press(a.paneIDAt(sx, sy), 1, traceevent.Mods{}, true))
	p, _, ok := a.forwardedPaneAt(sx, sy)
	if !ok {
		return
	}
	a.menu.Close()
	a.ascendPane(p)
}

// onForwardedLeftDown moves pane focus and arms a boundary resize for a left
// press over a live URL view. The preload does not prevent the default, so
// in-page interaction still reaches the page. The grab band's inner half sits on
// the live view, so such a drag could otherwise never start.
func (a *App) onForwardedLeftDown(sx, sy float64) {
	a.emit(traceevent.Press(a.paneIDAt(sx, sy), 0, traceevent.Mods{}, true))
	p, r, ok := a.forwardedPaneAt(sx, sy)
	if !ok {
		return
	}
	a.focusToPane(p)
	a.armLeftResize(r, sx, sy)
}

// onForwardedContextMenu focuses the pane a live URL view's native context menu
// opened on: the preload forwards a right-press only once it becomes a drag, so
// the menu is all the renderer hears. Keyed by pane id, since the bar-circle
// door has no cursor.
func (a *App) onForwardedContextMenu(paneID string) {
	p := a.tree.FindPane(paneID)
	if p == nil {
		return
	}
	a.focusToPane(p)
}

// onRightMove is the only path that mutates the tree mid-drag; the rest preview.
func (a *App) onRightMove(sx, sy float64) {
	rd := a.rightDrag
	if rd == nil {
		return
	}
	rd.curX = sx
	rd.curY = sy
	switch rd.kind {
	case rightDragTileCenter:
		rd.cursorInCenter = inTileCenter(rd.tileNode, rd.tilePane, rd.tilePaneR, sx, sy)
		a.advanceCloneDrag(sx, sy)
	case rightDragTileResize:
		rd.tileNewX, rd.tileNewY, rd.tileNewW, rd.tileNewH = tileResizeFromPin(rd, sx, sy)
	}
	a.draw()
}

func (a *App) tileAtScreen(p *pane.Pane, r pane.Rect, sx, sy float64) *gridwellv1.Tile {
	cellX, cellY := cellAtScreen(p, r, sx, sy)
	return a.tileAtCell(p, cellX, cellY)
}

// armTileGesture arms the same model for every tile kind: the center 1/3 by 1/3
// clones or links through a.dragging past the threshold, and everything outside
// resizes, which has no destination and so ignores the intent.
func (a *App) armTileGesture(p *pane.Pane, r pane.Rect, n *gridwellv1.Tile, sx, sy float64, intent dragdrop.Intent) {
	common := rightDragState{
		startX:     sx,
		startY:     sy,
		curX:       sx,
		curY:       sy,
		tilePaneID: p.ID,
		tileNode:   n,
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

func inTileCenter(n *gridwellv1.Tile, p *pane.Pane, r pane.Rect, sx, sy float64) bool {
	ps := paneToDragdrop(p, r)
	cx, cy := ps.ScreenToCell(sx, sy)
	return dragdrop.InTileCenter(n.X, n.Y, n.W, n.H, cx, cy)
}

func tileResizeAnchors(n *gridwellv1.Tile, p *pane.Pane, r pane.Rect, sx, sy float64) (
	pinX, pinY, origMovingX, origMovingY, clickCellX, clickCellY int64,
) {
	ps := paneToDragdrop(p, r)
	cxF, cyF := ps.ScreenToCell(sx, sy)
	a := dragdrop.ResizeAnchorsFor(n.X, n.Y, n.W, n.H, cxF, cyF)
	return a.PinX, a.PinY, a.OrigMovingX, a.OrigMovingY, a.ClickCellX, a.ClickCellY
}

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

// advanceCloneDrag runs the one ghost promotion the left-drag path runs.
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
		// The same DecideDrop verdict commitRightClone uses, off the same
		// d.intent, so preview and commit cannot diverge.
		a.previewDrop(d, sx, sy)
	}
	d.curScreenX = sx
	d.curScreenY = sy
}

// armRightClone primes a.dragging for a drag out of tile n's center zone. The
// ghost materializes only past dragThreshold, and the original stays visible,
// because both intents create.
func (a *App) armRightClone(p *pane.Pane, r pane.Rect, n *gridwellv1.Tile, sx, sy float64, intent dragdrop.Intent) {
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

// commitTileCenter no-ops on a bare release: the center is a grab handle.
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

// commitRightClone deletes, clones, links, or snaps the ghost back.
func (a *App) commitRightClone(d *dragState, sx, sy float64) {
	// The same gatherer as the left-drag commit, so preview and commit share
	// one decision off d.intent.
	in, t, dropX, dropY := a.dropInputAt(d, sx, sy, true /* placement */)

	switch a.commitVerdict(in, d, t) {
	case dragdrop.DropDelete:
		// The source pane's + button is a trashcan during any drag.
		a.runDeleteTile(d, nil)
		a.ghost = nil
		a.draw()
		return
	case dragdrop.DropRejected:
		// No target, the same cell, or an occupied one: snap back.
		a.cancelDragSnapBack(d)
		return
	case dragdrop.DropLink:
		// Ctrl was held at the press: the destination gains a reference and the
		// source stays put, whatever namespace it landed in. The one link commit
		// the cross-namespace left-drag also uses.
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
	req := &gridwellv1.CloneTileRequest{
		TileId:     tileID,
		DestGridId: dstGridID,
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

// runDeleteTile takes a nil t from the button path; it only names a second grid.
func (a *App) runDeleteTile(d *dragState, t *dropTarget) {
	var dstGridID string
	if t != nil {
		dstGridID = t.gridID
	}
	req := &gridwellv1.DeleteTileRequest{TileId: d.tileID}
	// The row, and the tmux session the server kills behind it, are going.
	delete(a.shellAlive, d.tileID)
	delete(a.shellAliveProbing, d.tileID)
	// No snapback: the tile vanishes either way, so a failed delete putting the
	// row back on screen is the reconcile.
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

// commitTileResize goes through PlaceTile, the one placement writeback.
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
	req := &gridwellv1.PlaceTileRequest{
		TileId: n.Id,
		GridId: n.GridId,
		X:      rd.tileNewX,
		Y:      rd.tileNewY,
		W:      rd.tileNewW,
		H:      rd.tileNewH,
	}
	a.postTileMutate("PlaceTile", gid, func(ctx context.Context) (*gridwellv1.Tile, error) {
		return a.cl.PlaceTile(ctx, req)
	}, nil)
}

// flushDroppedSubtree runs before a split collapse removes the subtree.
func (a *App) flushDroppedSubtree(n pane.TreeNode) {
	pane.WalkLeaves(n, func(p *pane.Pane) {
		a.flushPaneBeforeDrop(p)
	})
}

// flushPaneBeforeDrop persists a pane's descended state before the pane goes:
// text edits and framing, a freeze for a live URL or shell stream.
func (a *App) flushPaneBeforeDrop(p *pane.Pane) {
	if p.ContentID() != "" {
		if g, ok := a.c.Grid(a.gridIDForPane(p)); ok {
			if file, ok := g.Tiles[p.ContentID()]; ok {
				a.saveTextBeforeAscent(p, file)
			}
		}
	}
	// The dropped pane never reaches the ascent save path or the settle
	// persister again, so flush its grid framing here.
	a.persistPaneFraming(p)
	// forgetPane closes the live streams, so nothing outlives the pane id.
	a.forgetPane(p.ID)
}

// leftResizeAxis is one armed divider inside a left-button resize.
type leftResizeAxis struct {
	targetSplit *pane.Split
	splitDir    pane.Direction
	// crush is the close-threshold plan. The red preview and the release both
	// read crush.Red(), so there is one verdict.
	crush pane.CrushPlan
}

// leftResizeState owns closing too: crushing a side past the wall and releasing
// collapses it. A press grabs at most one divider per axis (pane.GrabDividers),
// so a corner is two ordinary resizes sharing one press, not a gesture of its own.
type leftResizeState struct {
	axes []leftResizeAxis
	// curX/curY is read by both the preview and the release, so the red warning
	// cannot mark a side the release does not collapse. At zero a bare click
	// would read (0,0), past the wall, and close a pane, so it arms to the press.
	curX, curY float64
}

// armLeftResize arms one resize per axis grabbed, true when anything armed.
func (a *App) armLeftResize(r pane.Rect, sx, sy float64) bool {
	grab, divs := a.dividerGrab(r, sx, sy)
	// dividerResizeCursor shares this gating, so the cursor cannot disagree.
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
	// Park live surfaces now; canvasGesture reads leftResize. Half the
	// grab band can sit over a view that would eat the next mousemove.
	a.draw()
	return true
}

// dividerResizeCursor goes through the same gesture.ResizeAffordance
// armLeftResize does, so the cursor appears exactly where a left-drag would
// resize, which is what makes the 10px band over a 1px line discoverable.
func (a *App) dividerResizeCursor(sx, sy float64) string {
	_, r, ok := a.paneAtScreen(sx, sy)
	if !ok {
		return ""
	}
	grab, _ := a.dividerGrab(r, sx, sy)
	_, cursor := gesture.ResizeAffordance(grab)
	return cursor
}

// onLeftResizeMove applies the move one armed axis at a time, so a corner grab
// moves both from the one cursor. The cascade compresses the adjacent pane to
// its minimum first, then the next, so the move itself never closes a pane.
func (a *App) onLeftResizeMove(sx, sy float64) {
	lr := a.leftResize
	if lr == nil {
		return
	}
	lr.curX, lr.curY = sx, sy
	// Each axis re-reads the tree, so the second sees the first's ratios.
	for i := range lr.axes {
		ax := &lr.axes[i]
		cursor := sx
		if ax.splitDir == pane.Horizontal {
			cursor = sy
		}
		// Fold the move into the red state before the layout follows the cursor:
		// the pre-move layout tells a deeper press from a back-off.
		ax.crush.Update(a.tree.Root, a.rootLayoutRect(), ax.targetSplit, pane.MinPanePx, cursor)
		// Walled at the pane minimum: the drag never collapses, the release does.
		pane.ResizeThrough(a.tree.Root, a.rootLayoutRect(), ax.targetSplit, cursor, pane.MinPanePx)
	}
	a.draw()
}

// finishLeftResize closes what the drag pressed, reading the same stored red
// state the preview drew. A corner grab can red a segment that lies inside one
// the other axis closes, so a segment already gone is skipped.
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

// commitSwap no-ops on a same-pane or off-canvas release.
func (a *App) commitSwap(rd *rightDragState, sx, sy float64) {
	destPane, _, ok := a.paneAtScreen(sx, sy)
	if !ok || destPane.ID == rd.originPaneID {
		return
	}
	_ = a.tree.Swap(rd.originPaneID, destPane.ID)
	_ = a.tree.SetFocus(rd.originPaneID)
}

// commitSplit opens the new pane on whichever side of the border the cursor
// traveled to, in the pane it is in at release. A sub-threshold drag or a
// position that would leave a child under pane.MinPanePx cancels silently.
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
	// A new pane is a clone of the source, deliberately: a split means another
	// view of where I am. A content frame is the exception, because there is one
	// native view or PTY attachment per tile and pane, so it ascends one level.
	if np != nil && np.ContentID() != "" {
		a.ascend(np, 1, true)
	}
}

// dividerGrab returns the divider slice too, so the caller can resolve the
// grab's indices. r must be the rect layoutPanes gave: a second copy would drift
// by the bar's height and GrabDividers' adjacency match would never fire.
func (a *App) dividerGrab(r pane.Rect, sx, sy float64) (pane.DividerGrab, []pane.Divider) {
	divs := pane.Dividers(a.tree, a.rootLayoutRect(), resizeBandPx)
	return pane.GrabDividers(divs, r, resizeBandPx, sx, sy), divs
}

// paneToDragdrop is where the five fields are gathered, once.
func paneToDragdrop(p *pane.Pane, r pane.Rect) dragdrop.Pane {
	return dragdrop.Pane{
		ScreenX: r.X, ScreenY: r.Y, ScreenW: r.W, ScreenH: r.H,
		Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom, CellPx: cellPx,
	}
}
