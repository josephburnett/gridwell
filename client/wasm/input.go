//go:build js && wasm

package main

import (
	"fmt"
	"math"
	"strings"
	"syscall/js"

	"github.com/josephburnett/ascent/client/anim"
	"github.com/josephburnett/ascent/client/dragdrop"
	"github.com/josephburnett/ascent/client/pane"
	"github.com/josephburnett/ascent/client/zoomtrans"
	"github.com/josephburnett/ascent/internal/rpc"
)

// Animation durations in milliseconds. Tuned for "stone settling" feel.
const (
	snapMs     = 110.0
	snapBackMs = 220.0

	// splitGrowMs is how long the "new pane grows out of the chosen
	// edge" animation takes after a single right-click.
	splitGrowMs = 220.0
)

// rightDragKind tags an in-flight right-button gesture once it's been
// classified on mousedown.
type rightDragKind int

const (
	rightDragNone rightDragKind = iota
	rightDragResize
	rightDragSwap
)

// rightDragState tracks an in-progress right-button gesture (resize a
// divider, swap two panes, or — if it never crosses the drag threshold
// — split the pane via single right-click).
type rightDragState struct {
	kind        rightDragKind
	startX, startY float64
	curX, curY     float64
	started     bool

	// Resize-only.
	targetSplit  *pane.Split
	splitDir     pane.Direction
	container    paneRect
	initialRatio float64

	// Swap-only.
	originPaneID string
}

// rightCloseThreshold is how thin (in screen px) a side of a divider
// has to be at release before we commit a close. Tied to paneBorderPx
// so the gesture is "drag until the borders touch."
const rightCloseThreshold = 2 * paneBorderPx

// installCanvasInput attaches mouse listeners to the canvas. Ascent is
// mouse-only by design — every gesture has a pointer equivalent and the
// keyboard is reserved for future text-editing modes (e.g., the markdown
// editor that's still TODO).
func (a *App) installCanvasInput() {
	a.canvas.Call("addEventListener", "wheel", js.FuncOf(a.onWheel))
	a.canvas.Call("addEventListener", "mousedown", js.FuncOf(a.onMouseDown))
	a.canvas.Call("addEventListener", "mousemove", js.FuncOf(a.onMouseMove))
	a.canvas.Call("addEventListener", "mouseup", js.FuncOf(a.onMouseUp))
	// Suppress the browser's context menu on the canvas without binding
	// any right-click behavior of our own — input is left-click only.
	a.canvas.Call("addEventListener", "contextmenu", js.FuncOf(func(this js.Value, args []js.Value) any {
		args[0].Call("preventDefault")
		return nil
	}))
}

// paneAtScreen returns the pane (and its rect) under the given screen coords,
// or (nil, paneRect{}, false) if no pane covers the point.
func (a *App) paneAtScreen(sx, sy float64) (*pane.Pane, paneRect, bool) {
	rects := a.layoutPanes()
	for id, r := range rects {
		if sx >= r.X && sy >= r.Y && sx < r.X+r.W && sy < r.Y+r.H {
			return a.tree.FindPane(id), r, true
		}
	}
	return nil, paneRect{}, false
}

// cellAtScreen returns the integer cell that contains screen point (sx, sy)
// inside the given pane. Uses floor (which cell is the cursor *in?*), not
// round — round-half made clicks in the lower-right half of a node miss.
func cellAtScreen(p *pane.Pane, r paneRect, sx, sy float64) (int64, int64) {
	ps := dragdrop.Pane{
		ScreenX: r.X, ScreenY: r.Y, ScreenW: r.W, ScreenH: r.H,
		Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom, CellPx: cellPx,
	}
	cx, cy := ps.ScreenToCell(sx, sy)
	return int64(math.Floor(cx)), int64(math.Floor(cy))
}

// nodeAtCell returns the node in the pane's grid that covers the given cell,
// or nil. Used for click hit-testing.
func (a *App) nodeAtCell(p *pane.Pane, cellX, cellY int64) *rpc.Node {
	gid := a.gridIDForPath(p.Path)
	g, ok := a.c.Grid(gid)
	if !ok {
		return nil
	}
	for _, n := range g.Nodes {
		if cellX >= n.X && cellX < n.X+n.W && cellY >= n.Y && cellY < n.Y+n.H {
			nn := n
			return &nn
		}
	}
	return nil
}

func (a *App) onWheel(this js.Value, args []js.Value) any {
	args[0].Call("preventDefault")
	dy := args[0].Get("deltaY").Float()
	sx, sy := mouseXY(args[0], a.canvas)
	p, _, ok := a.paneAtScreen(sx, sy)
	if !ok {
		return nil
	}
	// Inside a focused file the wheel scrolls vertically. The zoom is
	// fixed for the duration of the visit; combining zoom and scroll on
	// one gesture mixed badly. Text mode falls through to the textarea
	// (which gets the wheel event natively) — control reaches here only
	// for rendered mode where the canvas is on top.
	if p.FileFocus != 0 {
		if p.FileMode == "rendered" {
			z := nonzero(p.FileZoom)
			p.FileScrollY += dy / z
			if p.FileScrollY < 0 {
				p.FileScrollY = 0
			}
			a.draw()
			a.scheduleURLUpdate()
		}
		_ = sx
		_ = sy
		return nil
	}
	// Smooth zoom centered on the cursor: amount scales with deltaY so a
	// fast scroll covers more range, but capped per event.
	step := dy / 200.0
	if step > 0.5 {
		step = 0.5
	}
	if step < -0.5 {
		step = -0.5
	}
	factor := math.Pow(zoomFactor, -step*4)
	z := p.Zoom * factor
	if z < zoomMin {
		z = zoomMin
	}
	if z > zoomMax {
		z = zoomMax
	}
	p.Zoom = z
	a.draw()
	a.scheduleURLUpdate()
	return nil
}

func (a *App) onMouseDown(this js.Value, args []js.Value) any {
	sx, sy := mouseXY(args[0], a.canvas)
	// Block all input gestures while a viewport transition is animating
	// — keeps the tree and viewport state atomic across the zoom.
	if a.transition != nil {
		return nil
	}
	p, r, ok := a.paneAtScreen(sx, sy)
	if !ok {
		return nil
	}
	prevFocus := a.tree.Focus
	_ = a.tree.SetFocus(p.ID)
	if a.tree.Focus != prevFocus {
		// Focus moved → file-mode chrome must follow. The textarea
		// overlay only ever lives over the focused pane, so without
		// this call a click on a sibling pane in text mode would leave
		// the textarea stranded.
		a.refreshFileOverlay()
		a.draw()
	}
	button := args[0].Get("button").Int()
	if button == 2 {
		args[0].Call("preventDefault")
		a.onRightDown(p, r, sx, sy)
		return nil
	}
	if button != 0 {
		return nil
	}

	// In file-focus mode the lower-right button is a text/rendered toggle
	// rather than the + creation menu.
	if p.FileFocus != 0 {
		if pointInPlus(r, sx, sy) {
			a.onToggleFileMode(p)
			return nil
		}
		// Rendered mode: drag pans the file content (no textarea over us).
		// Text mode: the textarea covers the pane and handles drag itself.
		if p.FileMode == "rendered" {
			a.dragging = &dragState{
				originPaneID: p.ID,
				nodeID:       0,
				startScreenX: sx,
				startScreenY: sy,
				curScreenX:   sx,
				curScreenY:   sy,
			}
		}
		return nil
	}

	// Click on the + button toggles the menu for this pane.
	if pointInPlus(r, sx, sy) {
		if a.menuOpen && a.menuPaneID == p.ID {
			a.menuOpen = false
		} else {
			a.menuOpen = true
			a.menuPaneID = p.ID
			a.menuHover = -1
		}
		a.draw()
		return nil
	}
	// Click anywhere inside the open menu picks an item; clicks elsewhere
	// dismiss it.
	if a.menuOpen && a.menuPaneID == p.ID {
		idx := menuItemAt(r, sx, sy)
		if idx >= 0 {
			a.handleMenuItem(p, r, idx)
			a.menuOpen = false
			a.draw()
			return nil
		}
		a.menuOpen = false
		a.draw()
		// fall through so the click also pans / selects
	}

	cellX, cellY := cellAtScreen(p, r, sx, sy)
	n := a.nodeAtCell(p, cellX, cellY)
	clone := args[0].Get("altKey").Bool()
	a.dragging = &dragState{
		originPaneID: p.ID,
		nodeID:       0,
		startScreenX: sx,
		startScreenY: sy,
		curScreenX:   sx,
		curScreenY:   sy,
		clone:        clone,
	}
	if n != nil {
		a.dragging.nodeID = n.ID
		ps := dragdrop.Pane{
			ScreenX: r.X, ScreenY: r.Y, ScreenW: r.W, ScreenH: r.H,
			Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom, CellPx: cellPx,
		}
		cx, cy := ps.ScreenToCell(sx, sy)
		a.dragging.cellOffsetX = cx - float64(n.X)
		a.dragging.cellOffsetY = cy - float64(n.Y)
		a.dragging.snapshotNode = *n
		a.dragging.originScreenX, a.dragging.originScreenY = ps.CellToScreen(float64(n.X), float64(n.Y))
		a.dragging.originPaneRect = r
	}
	return nil
}

func (a *App) onMouseMove(this js.Value, args []js.Value) any {
	sx, sy := mouseXY(args[0], a.canvas)
	// Right-button gestures take precedence so a drag that started on
	// the right button doesn't accidentally invoke left-button code
	// paths (e.g., menu hover) below.
	if a.rightDrag != nil {
		// If the right button has been released somewhere we didn't
		// see (e.g., outside the canvas), commit the gesture as if
		// mouseup had fired.
		if buttons := args[0].Get("buttons").Int(); buttons&2 == 0 {
			a.finishRightDrag(sx, sy)
			return nil
		}
		a.onRightMove(sx, sy)
		return nil
	}
	// Track menu hover regardless of drag state.
	if a.menuOpen {
		_, r, ok := a.paneAtScreen(sx, sy)
		hover := -1
		if ok {
			hover = menuItemAt(r, sx, sy)
		}
		if hover != a.menuHover {
			a.menuHover = hover
			a.draw()
		}
	}
	if a.dragging == nil {
		return nil
	}
	d := a.dragging
	// Promote to "started" once cursor has moved past the threshold.
	if !d.started {
		dxs := sx - d.startScreenX
		dys := sy - d.startScreenY
		if dxs*dxs+dys*dys >= dragThreshold*dragThreshold {
			d.started = true
			// On node drag, materialize the ghost; for moves, also hide
			// the original at its stored position so we don't see two
			// copies of the same stone.
			if d.nodeID != 0 {
				a.ghost = &ghost{
					node:    d.snapshotNode,
					paneID:  d.originPaneID,
					screenX: d.originScreenX,
					screenY: d.originScreenY,
				}
				if !d.clone {
					a.hiddenObjectID = d.snapshotNode.ObjectID
					a.hiddenPaneID = d.originPaneID
				}
			}
		} else {
			return nil
		}
	}
	if d.nodeID == 0 {
		// Pan the source pane smoothly. In file-rendered mode the drag
		// scrolls the file's logical content; in grid mode it pans the
		// parent-grid view.
		focused := a.tree.FindPane(d.originPaneID)
		if focused != nil {
			if focused.FileFocus != 0 && focused.FileMode == "rendered" {
				z := nonzero(focused.FileZoom)
				focused.FileScrollX -= (sx - d.curScreenX) / z
				focused.FileScrollY -= (sy - d.curScreenY) / z
				if focused.FileScrollY < 0 {
					focused.FileScrollY = 0
				}
				if focused.FileScrollX < 0 {
					focused.FileScrollX = 0
				}
			} else {
				cellSize := cellPx * focused.Zoom
				focused.Cx -= (sx - d.curScreenX) / cellSize
				focused.Cy -= (sy - d.curScreenY) / cellSize
			}
		}
	} else if a.ghost != nil {
		// Move the ghost: offset cursor by the grab point so the original
		// click location stays "under the finger" relative to the node.
		hostPane, hostRect, ok := a.paneAtScreen(sx, sy)
		if !ok {
			// Cursor outside any pane — pin the ghost to the source pane
			// using the source pane's transform.
			src := a.tree.FindPane(d.originPaneID)
			if src != nil {
				ps := dragdrop.Pane{
					ScreenX: d.originPaneRect.X, ScreenY: d.originPaneRect.Y,
					ScreenW: d.originPaneRect.W, ScreenH: d.originPaneRect.H,
					Cx: src.Cx, Cy: src.Cy, Zoom: src.Zoom, CellPx: cellPx,
				}
				cx, cy := ps.ScreenToCell(sx, sy)
				topLeftX, topLeftY := ps.CellToScreen(cx-d.cellOffsetX, cy-d.cellOffsetY)
				a.ghost.paneID = d.originPaneID
				a.ghost.screenX = topLeftX
				a.ghost.screenY = topLeftY
			}
		} else {
			ps := dragdrop.Pane{
				ScreenX: hostRect.X, ScreenY: hostRect.Y,
				ScreenW: hostRect.W, ScreenH: hostRect.H,
				Cx: hostPane.Cx, Cy: hostPane.Cy, Zoom: hostPane.Zoom, CellPx: cellPx,
			}
			cx, cy := ps.ScreenToCell(sx, sy)
			topLeftX, topLeftY := ps.CellToScreen(cx-d.cellOffsetX, cy-d.cellOffsetY)
			a.ghost.paneID = hostPane.ID
			a.ghost.screenX = topLeftX
			a.ghost.screenY = topLeftY
		}
	}
	d.curScreenX = sx
	d.curScreenY = sy
	a.draw()
	return nil
}

func (a *App) onMouseUp(this js.Value, args []js.Value) any {
	// Right-button release commits a pending pane-management gesture.
	if a.rightDrag != nil && args[0].Get("button").Int() == 2 {
		sx, sy := mouseXY(args[0], a.canvas)
		a.finishRightDrag(sx, sy)
		return nil
	}
	if a.dragging == nil {
		return nil
	}
	d := a.dragging
	a.dragging = nil
	sx, sy := mouseXY(args[0], a.canvas)

	// Bare click (no movement): navigation.
	if !d.started {
		focused := a.tree.FindPane(d.originPaneID)
		if focused == nil {
			a.draw()
			return nil
		}
		r := paneRectFor(a, focused)
		// Try descent/ascent first — a click on a well, a file, or in
		// the edge band kicks off navigation. Selection only applies to
		// other cases (e.g., clicking an image preview to outline it).
		if a.attemptDescentOrAscent(focused, r, sx, sy) {
			a.scheduleURLUpdate()
			return nil
		}
		cellX, cellY := cellAtScreen(focused, r, sx, sy)
		if n := a.nodeAtCell(focused, cellX, cellY); n != nil {
			a.selectedNodeID[focused.ID] = n.ID
		} else {
			delete(a.selectedNodeID, focused.ID)
		}
		a.draw()
		a.scheduleURLUpdate()
		return nil
	}

	// Pan drag end: just persist viewport state.
	if d.nodeID == 0 {
		a.scheduleURLUpdate()
		a.draw()
		return nil
	}

	// Node move/clone drag. Resolve the drop pane and snapped drop cell.
	srcPane := a.tree.FindPane(d.originPaneID)
	if srcPane == nil {
		a.cancelDragSnapBack(d)
		return nil
	}
	destPane, destRect, ok := a.paneAtScreen(sx, sy)
	if !ok {
		// Released outside any pane: snap-back.
		a.cancelDragSnapBack(d)
		return nil
	}
	dpscreen := dragdrop.Pane{
		ScreenX: destRect.X, ScreenY: destRect.Y, ScreenW: destRect.W, ScreenH: destRect.H,
		Cx: destPane.Cx, Cy: destPane.Cy, Zoom: destPane.Zoom, CellPx: cellPx,
	}
	dcx, dcy := dpscreen.ScreenToCell(sx, sy)
	dropX := dragdrop.SnapToCell(dcx - d.cellOffsetX)
	dropY := dragdrop.SnapToCell(dcy - d.cellOffsetY)

	// Animate ghost from current position to the snap target in the dest
	// pane's screen coords.
	targetX, targetY := dpscreen.CellToScreen(float64(dropX), float64(dropY))
	if a.ghost != nil {
		// Re-anchor the ghost paneID to the destination pane so the
		// rendering during animation lives in dest pane coordinates.
		a.ghost.paneID = destPane.ID
	}
	a.startSnap(targetX, targetY, snapMs)

	srcRect := paneRectFor(a, srcPane)
	srcView := a.paneViewRect(srcPane, dragdrop.Pane{
		ScreenX: srcRect.X, ScreenY: srcRect.Y, ScreenW: srcRect.W, ScreenH: srcRect.H,
		Cx: srcPane.Cx, Cy: srcPane.Cy, Zoom: srcPane.Zoom, CellPx: cellPx,
	})
	dstView := a.paneViewRect(destPane, dpscreen)
	dstGridID := a.gridIDForPath(destPane.Path)

	go func() {
		var status int
		if d.clone {
			req := rpc.CloneNodeRequest{
				Path: rpc.Path{WellIDs: srcPane.Path}, ViewRect: srcView,
				NodeID:     d.nodeID,
				DestGridID: dstGridID, DestPath: rpc.Path{WellIDs: destPane.Path}, DestViewRect: dstView,
				X: dropX, Y: dropY,
			}
			var resp rpc.NodeResponse
			status, _ = postJSON("/rpc/CloneNode", req, &resp)
		} else {
			req := rpc.MoveNodeRequest{
				Path: rpc.Path{WellIDs: srcPane.Path}, ViewRect: srcView,
				NodeID:     d.nodeID,
				DestGridID: dstGridID, DestPath: rpc.Path{WellIDs: destPane.Path}, DestViewRect: dstView,
				X: dropX, Y: dropY,
			}
			var resp rpc.MoveNodeResponse
			status, _ = postJSON("/rpc/MoveNode", req, &resp)
		}
		if status != 200 {
			// Server rejected the drop. Snap the ghost back to origin so
			// the user sees the stone return rather than vanish.
			a.snapBackToOrigin(d)
			return
		}
		a.fetchGrid(a.gridIDForPath(srcPane.Path))
		a.fetchGrid(dstGridID)
	}()
	a.draw()
	return nil
}

// startSnap animates the active ghost from its current position to (toX, toY)
// over the given duration. Replaces any prior animation.
func (a *App) startSnap(toX, toY, duration float64) {
	if a.ghost == nil {
		return
	}
	a.animation = &anim.Animation{
		FromX:      a.ghost.screenX,
		FromY:      a.ghost.screenY,
		ToX:        toX,
		ToY:        toY,
		StartMs:    nowMs(),
		DurationMs: duration,
	}
	a.scheduleFrame()
}

// cancelDragSnapBack runs the snap-back-to-origin animation when a drop is
// abandoned (released outside any pane, or the source pane vanished).
func (a *App) cancelDragSnapBack(d *dragState) {
	if a.ghost == nil {
		// Drag never crossed the threshold — nothing to animate.
		a.draw()
		return
	}
	a.snapBackToOrigin(d)
}

// snapBackToOrigin starts an animation from the ghost's current position
// back to the original location of the dragged node. Used both for failed
// server commits and for drops outside any pane.
func (a *App) snapBackToOrigin(d *dragState) {
	if a.ghost == nil {
		return
	}
	a.ghost.paneID = d.originPaneID
	a.animation = &anim.Animation{
		FromX:      a.ghost.screenX,
		FromY:      a.ghost.screenY,
		ToX:        d.originScreenX,
		ToY:        d.originScreenY,
		StartMs:    nowMs(),
		DurationMs: snapBackMs,
	}
	a.scheduleFrame()
}

func paneRectFor(a *App, p *pane.Pane) paneRect {
	rects := a.layoutPanes()
	if r, ok := rects[p.ID]; ok {
		return r
	}
	return paneRect{}
}

// attemptDescentOrAscent routes a bare left-click (no drag) at (sx, sy)
// inside pane p to the right navigation gesture.
//
//   - In the edge band: ascend (file ascent if file-focused; well ascent
//     otherwise; AscendAtRoot at the user's root).
//   - On a well: descend into the well.
//   - On a markdown file: descend into the file.
//   - Otherwise: no-op (selection is handled by the bare-click path
//     in onMouseUp before this is invoked).
//
// Edge takes priority over node hits so the user always has a reachable
// ascent target even when a node sits along the edge.
//
// Returns true if a navigation gesture was performed (caller should skip
// further interpretation of the click).
func (a *App) attemptDescentOrAscent(p *pane.Pane, r paneRect, sx, sy float64) bool {
	pscreen := dragdrop.Pane{
		ScreenX: r.X, ScreenY: r.Y, ScreenW: r.W, ScreenH: r.H,
		Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom, CellPx: cellPx,
	}
	if dragdrop.IsInEdgeZone(pscreen, sx, sy, dragdrop.EdgeBand(pscreen)) {
		switch {
		case p.FileFocus != 0:
			a.startFileAscent(p)
		case len(p.Path) > 0:
			a.startAscent(p)
		default:
			go func() {
				var resp rpc.AscendAtRootResponse
				if _, err := postJSON("/rpc/AscendAtRoot", rpc.AscendAtRootRequest{}, &resp); err == nil {
					a.user.RootGridID = resp.NewRootGridID
					a.fetchGrid(resp.NewRootGridID)
				}
			}()
		}
		return true
	}
	if p.FileFocus != 0 {
		// Inside a file, a click that's not on the toggle and not on the
		// edge isn't navigation — it's either pan-drag (handled in
		// mousemove) or a non-action click.
		return false
	}
	cellX, cellY := cellAtScreen(p, r, sx, sy)
	hit := a.nodeAtCell(p, cellX, cellY)
	if hit == nil {
		return false
	}
	switch {
	case hit.Type == "well" && !hit.Capped:
		a.startDescent(p, hit)
		return true
	case hit.Type == "file" && hit.MimeType == "text/markdown":
		a.startFileDescent(p, hit)
		return true
	}
	return false
}

// totalTransitionMs is the total wall-clock duration of a descent or
// ascent transition. The same value is used for both so the UX is
// symmetric in feel.
const totalTransitionMs = 350.0

// zoomDistFactor scales log-zoom distance to a "perceived px" unit so we
// can apportion animation time between pan and zoom phases. Tuned so a
// zoom by factor e ≈ 256 px-equivalent — about four cells. Bigger than
// pan distances tend to be in practice, so zoom phases get the bulk of
// the time, which matches the user's intent that the zoom is "the
// action" and the pan is "the setup".
const zoomDistFactor = 4.0

// panDist returns the pan motion distance in screen pixels at the given
// zoom level. dx, dy are in cell units.
func panDist(dx, dy, zoom float64) float64 {
	return math.Hypot(dx, dy) * cellPx * zoom
}

// zoomDist returns the log-zoom distance scaled to the same px-equivalent
// units as panDist, so they can be compared directly.
func zoomDist(z1, z2 float64) float64 {
	if z1 <= 0 || z2 <= 0 {
		return 0
	}
	return math.Abs(math.Log(z2/z1)) * cellPx * zoomDistFactor
}

// startAscent zooms a pane out of a child grid and back to the parent
// grid in two concurrent-motion segments: one that finishes the child's
// trip to the calibrated path-switch state, and one in the parent that
// pans-and-zooms back to the user's saved viewport in a single motion.
// Pan and zoom interpolate together within each segment, so they begin
// and end at the same moment regardless of which has more "distance".
//
// If no saved state is available (cleared localStorage, reload), we land
// on the well at zoom 1.
func (a *App) startAscent(p *pane.Pane) {
	if len(p.Path) == 0 {
		return
	}
	// Walk back from the leaf looking for the deepest ancestor we can
	// actually animate from. If a well row is missing or its parent
	// grid is unreadable, skip past it and try the level above. Falls
	// back to instant snap-to-root when nothing in the path resolves.
	level := len(p.Path) - 1
	var well rpc.Node
	for ; level >= 0; level-- {
		parentPath := p.Path[:level]
		parentGridID := a.gridIDForPath(parentPath)
		g, ok := a.c.Grid(parentGridID)
		if !ok {
			a.fetchGrid(parentGridID)
			continue
		}
		w, ok := g.Nodes[p.Path[level]]
		if !ok {
			continue
		}
		well = w
		break
	}
	if level < 0 {
		a.instantAscend(p, nil)
		return
	}
	if level != len(p.Path)-1 {
		// We skipped one or more missing levels. The animation math
		// expects to be ascending out of the leaf grid; jumping mid-
		// path would render badly. Snap directly to the resolved level
		// and let the user ascend again from there if they want.
		a.instantAscend(p, append([]int64(nil), p.Path[:level]...))
		return
	}
	parentPath := append([]int64(nil), p.Path[:level]...)
	r := paneRectFor(a, p)
	from := zoomtrans.Endpoints{
		Path: append([]int64(nil), p.Path...),
		Cx:   p.Cx, Cy: p.Cy, Zoom: p.Zoom,
	}
	w := zoomtrans.Well{
		ID: well.ID, X: well.X, Y: well.Y, W: well.W, H: well.H,
		ViewX: well.ViewX, ViewY: well.ViewY,
	}
	mid, switchTo := zoomtrans.Ascent(from, w, parentPath, r.W, r.H, cellPx)

	saved := a.popPaneState(p.ID)
	if saved == nil {
		saved = &paneState{Cx: switchTo.Cx, Cy: switchTo.Cy, Zoom: 1.0}
	}

	// Distances in shared px-equivalent units so SplitN can apportion
	// time so each phase moves at a comparable visual speed.
	childDist := panDist(mid.Cx-from.Cx, mid.Cy-from.Cy, from.Zoom) +
		zoomDist(from.Zoom, mid.Zoom)
	parentDist := panDist(saved.Cx-switchTo.Cx, saved.Cy-switchTo.Cy, saved.Zoom) +
		zoomDist(switchTo.Zoom, saved.Zoom)
	durations := anim.SplitN([]float64{childDist, parentDist}, totalTransitionMs)

	a.startTransition(&paneTransition{
		paneID: p.ID,
		segments: []transSegment{
			// Child grid: combined pan+zoom to land on calibrated state.
			{
				path:   from.Path,
				fromCx: from.Cx, fromCy: from.Cy, fromZoom: from.Zoom,
				toCx: mid.Cx, toCy: mid.Cy, toZoom: mid.Zoom,
				durationMs: durations[0],
			},
			// Parent grid: combined pan+zoom from well center back to saved.
			{
				path:   switchTo.Path,
				fromCx: switchTo.Cx, fromCy: switchTo.Cy, fromZoom: switchTo.Zoom,
				toCx: saved.Cx, toCy: saved.Cy, toZoom: saved.Zoom,
				durationMs: durations[1],
			},
		},
	})
}

// instantAscend is the fallback path when the parent grid isn't cached or
// the well row vanished. We just drop the last entry of the path; the user
// can wait for the parent to load and reposition manually.
func (a *App) instantAscend(p *pane.Pane, parentPath []int64) {
	a.popPaneState(p.ID) // discard whatever was saved; we can't honor it.
	p.Path = parentPath
	p.Cx, p.Cy, p.Zoom = 0, 0, 1.0
	delete(a.selectedNodeID, p.ID)
	a.draw()
	a.scheduleURLUpdate()
}

// startDescent pushes the pane's current state onto the saved-state stack
// and installs a single combined-motion transition into the well's
// child grid. Pan and zoom interpolate together so they finish at the
// same moment regardless of which has more "distance" — when the well is
// far from center, the camera moves faster to keep both motions in sync.
//
// Phases:
//   A. Combined pan+zoom in parent to (wellCenter, OvertakeZoom).
//   B. Instant install of the calibrated child state.
func (a *App) startDescent(p *pane.Pane, well *rpc.Node) {
	a.pushPaneState(p.ID, paneState{Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom})

	r := paneRectFor(a, p)
	from := zoomtrans.Endpoints{
		Path: append([]int64(nil), p.Path...),
		Cx:   p.Cx, Cy: p.Cy, Zoom: p.Zoom,
	}
	w := zoomtrans.Well{
		ID: well.ID, X: well.X, Y: well.Y, W: well.W, H: well.H,
		ViewX: well.ViewX, ViewY: well.ViewY,
	}
	mid, to := zoomtrans.Descent(from, w, r.W, r.H, cellPx)
	a.fetchGrid(well.ChildGridID)

	a.startTransition(&paneTransition{
		paneID: p.ID,
		segments: []transSegment{
			// Combined pan+zoom toward well center at OvertakeZoom.
			{
				path:   from.Path,
				fromCx: from.Cx, fromCy: from.Cy, fromZoom: from.Zoom,
				toCx: mid.Cx, toCy: mid.Cy, toZoom: mid.Zoom,
				durationMs: totalTransitionMs,
			},
			// Instant install of the calibrated child state at the path
			// switch — visually continuous because of the zoomtrans
			// calibration: a parent cell at switch == a child preview
			// cell, and the cell scale matches the new child zoom.
			{
				path:   to.Path,
				fromCx: to.Cx, fromCy: to.Cy, fromZoom: to.Zoom,
				toCx: to.Cx, toCy: to.Cy, toZoom: to.Zoom,
				durationMs: 0,
			},
		},
	})
}


// nonzero returns x or 1.0 if x is zero/negative. Saves a guard at every
// call site that divides by FileZoom.
func nonzero(x float64) float64 {
	if x <= 0 {
		return 1.0
	}
	return x
}

// startFileDescent zooms a pane into a markdown file in a single
// concurrent pan+zoom motion, then flips to file-editing mode. Unlike
// well descent, the path is not extended (the file lives in the parent
// grid as a leaf node), so the transition is one segment in the parent
// coordinate space; the visual landing is at OvertakeZoom on the file's
// footprint, after which the file-mode chrome takes over.
//
// The post-landing FileZoom is set to a comfortable reading scale,
// independent of the parent zoom, so the markdown isn't rendered at
// OvertakeZoom magnification (which produced the "huge" complaint).
// Yes there is a one-frame scale jump at the path-switch — accepted
// trade-off for a sane initial reading view.
func (a *App) startFileDescent(p *pane.Pane, file *rpc.Node) {
	a.pushPaneState(p.ID, paneState{Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom})

	r := paneRectFor(a, p)
	from := zoomtrans.Endpoints{
		Path: append([]int64(nil), p.Path...),
		Cx:   p.Cx, Cy: p.Cy, Zoom: p.Zoom,
	}
	w := zoomtrans.Well{
		ID: file.ID, X: file.X, Y: file.Y, W: file.W, H: file.H,
		ViewX: file.ViewX, ViewY: file.ViewY,
	}
	wellCx := float64(file.X) + float64(file.W)/2
	wellCy := float64(file.Y) + float64(file.H)/2
	target := zoomtrans.OvertakeZoom(w, r.W, r.H, cellPx)
	if target < from.Zoom {
		target = from.Zoom
	}

	// Eagerly fetch the blob so it's likely cached by the time the
	// transition lands.
	a.fetchBlob(file.BlobID)

	fileID := file.ID
	initialScroll := float64(file.ViewY)
	mode := "rendered"
	if last, ok := a.fileLastMode[fileID]; ok && last != "" {
		mode = last
	}
	a.startTransition(&paneTransition{
		paneID: p.ID,
		segments: []transSegment{
			// Single combined pan+zoom segment: pan to the file center
			// while simultaneously zooming to the overtake target.
			{
				path:   from.Path,
				fromCx: from.Cx, fromCy: from.Cy, fromZoom: from.Zoom,
				toCx: wellCx, toCy: wellCy, toZoom: target,
				durationMs: totalTransitionMs,
			},
		},
		onComplete: func() {
			fp := a.tree.FindPane(p.ID)
			if fp == nil {
				return
			}
			fp.FileFocus = fileID
			fp.FileMode = mode
			fp.FileScrollY = initialScroll
			fp.FileScrollX = 0
			fp.FileZoom = fileInitialZoom(r.W, r.H)
			a.refreshFileOverlay()
		},
	})
}

// startFileAscent reverses the file descent: animate zoom-out from the
// file's footprint back to the saved viewport, then clear FileFocus and
// save the file's content + scroll.
func (a *App) startFileAscent(p *pane.Pane) {
	if p.FileFocus == 0 {
		return
	}
	gid := a.gridIDForPath(p.Path)
	g, ok := a.c.Grid(gid)
	if !ok {
		// Parent grid not cached — give up gracefully.
		a.exitFileFocusInstant(p)
		return
	}
	file, ok := g.Nodes[p.FileFocus]
	if !ok {
		a.exitFileFocusInstant(p)
		return
	}
	r := paneRectFor(a, p)
	w := zoomtrans.Well{
		ID: file.ID, X: file.X, Y: file.Y, W: file.W, H: file.H,
		ViewX: file.ViewX, ViewY: file.ViewY,
	}
	wellCx := float64(file.X) + float64(file.W)/2
	wellCy := float64(file.Y) + float64(file.H)/2
	overtake := zoomtrans.OvertakeZoom(w, r.W, r.H, cellPx)
	if overtake > p.Zoom {
		overtake = p.Zoom
	}

	saved := a.popPaneState(p.ID)
	if saved == nil {
		saved = &paneState{Cx: wellCx, Cy: wellCy, Zoom: 1.0}
	}

	// Save before transition: capture the editor buffer (if text mode is
	// active) and post UpdateFileContent + SetNodeViewport. The animation
	// runs concurrently with the network round-trip; the user doesn't
	// have to wait.
	a.saveFileBeforeAscent(p, file)

	// Persist the mode the user is leaving in so previews and re-descent
	// honor the "however you left it" rule.
	if p.FileMode != "" {
		a.fileLastMode[file.ID] = p.FileMode
	}

	// Reset parent-grid zoom to the overtake value so the animation
	// begins from "well filling the pane", regardless of how the user
	// zoomed within the file. Then clear FileFocus so the chrome (toggle
	// button, textarea) goes away as the animation begins.
	p.Zoom = overtake
	p.Cx, p.Cy = wellCx, wellCy
	p.FileFocus = 0
	a.refreshFileOverlay()

	a.startTransition(&paneTransition{
		paneID: p.ID,
		segments: []transSegment{
			// Single combined pan+zoom segment back to the saved viewport.
			{
				path:   append([]int64(nil), p.Path...),
				fromCx: wellCx, fromCy: wellCy, fromZoom: overtake,
				toCx: saved.Cx, toCy: saved.Cy, toZoom: saved.Zoom,
				durationMs: totalTransitionMs,
			},
		},
	})
}

// exitFileFocusInstant is the fallback path when the parent grid isn't
// cached or the file row vanished while we were focused on it. We just
// clear FileFocus and reset the viewport to whatever was saved.
func (a *App) exitFileFocusInstant(p *pane.Pane) {
	saved := a.popPaneState(p.ID)
	p.FileFocus = 0
	if saved != nil {
		p.Cx, p.Cy, p.Zoom = saved.Cx, saved.Cy, saved.Zoom
	}
	a.refreshFileOverlay()
	a.draw()
	a.scheduleURLUpdate()
}

// saveFileBeforeAscent posts the editor buffer (if text mode is active)
// and the live scroll position back to the server. Failures are silently
// dropped; the user will see the local state on next descent and the
// server state otherwise.
func (a *App) saveFileBeforeAscent(p *pane.Pane, file rpc.Node) {
	gid := a.gridIDForPath(p.Path)
	r := paneRectFor(a, p)
	pscreen := dragdrop.Pane{
		ScreenX: r.X, ScreenY: r.Y, ScreenW: r.W, ScreenH: r.H,
		Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom, CellPx: cellPx,
	}
	view := a.paneViewRect(p, pscreen)
	scrollY := int64(p.FileScrollY + 0.5)

	// Capture the textarea contents (if any) before we tear it down.
	var buf string
	hasBuf := false
	if p.FileMode == "text" {
		ta := a.fileTextarea
		if !ta.IsNull() && !ta.IsUndefined() {
			buf = ta.Get("value").String()
			hasBuf = true
		}
	}

	go func() {
		// Update content first if the user was editing.
		if hasBuf {
			req := rpc.UpdateFileContentRequest{
				Path: rpc.Path{WellIDs: p.Path}, ViewRect: view,
				NodeID: file.ID, Data: []byte(buf),
			}
			var resp rpc.NodeResponse
			if _, err := postJSON("/rpc/UpdateFileContent", req, &resp); err == nil {
				a.c.PutBlob(resp.Node.BlobID, []byte(buf), "text/markdown")
			}
		}
		// Always update view_y so re-descent restores the scroll.
		vreq := rpc.SetNodeViewportRequest{
			Path: rpc.Path{WellIDs: p.Path}, ViewRect: view,
			NodeID: file.ID, ViewX: 0, ViewY: scrollY,
		}
		var vresp rpc.NodeResponse
		_, _ = postJSON("/rpc/SetNodeViewport", vreq, &vresp)
		a.fetchGrid(gid)
	}()
}

// handleMenuItem performs the action for the i'th menu item, with the
// pane and its rect for context.
func (a *App) handleMenuItem(p *pane.Pane, r paneRect, idx int) {
	if idx < 0 || idx >= len(menuItems) {
		return
	}
	item := menuItems[idx]
	switch item {
	case "well":
		a.createAtViewportCenter(p, r, "well", "", nil)
	case "markdown":
		a.createAtViewportCenter(p, r, "file", "text/markdown", []byte("# untitled\n"))
	case "url":
		val := js.Global().Call("prompt", "URL:")
		if val.IsNull() || val.IsUndefined() {
			return
		}
		s := val.String()
		if s == "" {
			return
		}
		a.createAtViewportCenter(p, r, "file", "text/uri-list", []byte(s))
	case "upload":
		a.openUpload(p, r)
	}
}

// createAtViewportCenter creates a node at the (rounded) cell nearest the
// pane's viewport center. For wells, mime/data are ignored; for files, both
// are used. The footprint is 1×1.
func (a *App) createAtViewportCenter(p *pane.Pane, r paneRect, kind, mime string, data []byte) {
	cellX := dragdrop.SnapToCell(p.Cx)
	cellY := dragdrop.SnapToCell(p.Cy)
	pscreen := dragdrop.Pane{
		ScreenX: r.X, ScreenY: r.Y, ScreenW: r.W, ScreenH: r.H,
		Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom, CellPx: cellPx,
	}
	view := a.paneViewRect(p, pscreen)
	gid := a.gridIDForPath(p.Path)
	go func() {
		if kind == "well" {
			req := rpc.CreateWellRequest{
				Path: rpc.Path{WellIDs: p.Path}, ViewRect: view,
				GridID: gid, X: cellX, Y: cellY, W: 1, H: 1,
			}
			var resp rpc.NodeResponse
			_, _ = postJSON("/rpc/CreateWell", req, &resp)
		} else {
			req := rpc.CreateFileRequest{
				Path: rpc.Path{WellIDs: p.Path}, ViewRect: view,
				GridID: gid, X: cellX, Y: cellY, W: 1, H: 1,
				MimeType: mime, Data: data,
			}
			var resp rpc.NodeResponse
			_, _ = postJSON("/rpc/CreateFile", req, &resp)
		}
		a.fetchGrid(gid)
	}()
}

// openUpload triggers the hidden <input type="file"> and, on selection,
// reads the file via FileReader and POSTs CreateFile.
func (a *App) openUpload(p *pane.Pane, r paneRect) {
	input := a.doc.Call("getElementById", "upload-input")
	if input.IsNull() || input.IsUndefined() {
		return
	}
	// Replace any prior onchange so we don't pile up handlers across
	// repeated uses.
	if a.uploadHandlerOK {
		a.uploadHandler.Release()
	}
	a.uploadHandler = js.FuncOf(func(this js.Value, args []js.Value) any {
		files := input.Get("files")
		if files.IsNull() || files.IsUndefined() || files.Length() == 0 {
			return nil
		}
		file := files.Index(0)
		name := file.Get("name").String()
		mime := file.Get("type").String()
		if mime == "" {
			mime = mimeFromName(name)
		}
		go a.uploadFileBytes(p, r, file, mime)
		// Clear so the same file can be re-selected next time.
		input.Set("value", "")
		return nil
	})
	a.uploadHandlerOK = true
	input.Set("onchange", a.uploadHandler)
	input.Call("click")
}

// uploadFileBytes reads the JS File object as bytes (via arrayBuffer Promise)
// and calls CreateFile.
func (a *App) uploadFileBytes(p *pane.Pane, r paneRect, file js.Value, mime string) {
	buf, err := await(file.Call("arrayBuffer"))
	if err != nil {
		return
	}
	u8 := js.Global().Get("Uint8Array").New(buf)
	n := u8.Get("length").Int()
	data := make([]byte, n)
	js.CopyBytesToGo(data, u8)

	cellX := dragdrop.SnapToCell(p.Cx)
	cellY := dragdrop.SnapToCell(p.Cy)
	pscreen := dragdrop.Pane{
		ScreenX: r.X, ScreenY: r.Y, ScreenW: r.W, ScreenH: r.H,
		Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom, CellPx: cellPx,
	}
	view := a.paneViewRect(p, pscreen)
	gid := a.gridIDForPath(p.Path)
	req := rpc.CreateFileRequest{
		Path: rpc.Path{WellIDs: p.Path}, ViewRect: view,
		GridID: gid, X: cellX, Y: cellY, W: 1, H: 1,
		MimeType: mime, Data: data,
	}
	var resp rpc.NodeResponse
	_, _ = postJSON("/rpc/CreateFile", req, &resp)
	a.fetchGrid(gid)
}

// mimeFromName picks a MIME type from the file extension when the browser
// did not set one. Limited to the v1 set.
func mimeFromName(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".md"), strings.HasSuffix(lower, ".markdown"):
		return "text/markdown"
	case strings.HasSuffix(lower, ".png"):
		return "image/png"
	case strings.HasSuffix(lower, ".jpg"), strings.HasSuffix(lower, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(lower, ".gif"):
		return "image/gif"
	case strings.HasSuffix(lower, ".webp"):
		return "image/webp"
	case strings.HasSuffix(lower, ".url"), strings.HasSuffix(lower, ".uri"):
		return "text/uri-list"
	}
	return ""
}


// mouseXY returns the click coordinates relative to the canvas.
func mouseXY(ev js.Value, canvas js.Value) (float64, float64) {
	rect := canvas.Call("getBoundingClientRect")
	x := ev.Get("clientX").Float() - rect.Get("left").Float()
	y := ev.Get("clientY").Float() - rect.Get("top").Float()
	return x, y
}

// silence unused.
var _ = fmt.Sprintf

// ----------------------------------------------------------------------
// Right-button: pane management
// ----------------------------------------------------------------------

// dividerHit returns the divider under (sx, sy) given a hit-band that's
// a few pixels wider than the rendered border so the user doesn't have
// to pixel-hunt. Returns nil if the cursor is not on any divider.
func (a *App) dividerHit(sx, sy float64) *pane.Divider {
	root := pane.Rect{X: 0, Y: 0, W: a.width, H: a.height}
	// Hit-band is generous: the visible border is paneBorderPx, the
	// hit zone is twice that on each side so it's easy to grab.
	band := 2 * paneBorderPx
	divs := pane.Dividers(a.tree, root, band)
	for i := range divs {
		d := divs[i]
		if sx >= d.Rect.X && sx < d.Rect.X+d.Rect.W &&
			sy >= d.Rect.Y && sy < d.Rect.Y+d.Rect.H {
			return &divs[i]
		}
	}
	return nil
}

// onRightDown classifies the gesture: divider drag (resize/close) vs
// pane-interior gesture (single click → split, drag → swap).
func (a *App) onRightDown(p *pane.Pane, r paneRect, sx, sy float64) {
	if a.splitAnim != nil {
		// Don't pile up gestures on top of an in-flight split animation.
		return
	}
	if d := a.dividerHit(sx, sy); d != nil {
		a.rightDrag = &rightDragState{
			kind:         rightDragResize,
			startX:       sx,
			startY:       sy,
			curX:         sx,
			curY:         sy,
			targetSplit:  d.Split,
			splitDir:     d.Dir,
			container:    paneRect{X: d.ContainerRect.X, Y: d.ContainerRect.Y, W: d.ContainerRect.W, H: d.ContainerRect.H},
			initialRatio: d.Split.Ratio,
		}
		return
	}
	a.rightDrag = &rightDragState{
		kind:         rightDragSwap,
		startX:       sx,
		startY:       sy,
		curX:         sx,
		curY:         sy,
		originPaneID: p.ID,
	}
	_ = r
}

// onRightMove updates the in-flight right-button gesture. Resize is
// applied live (set the split's ratio so the user sees the dividers
// glide under the cursor); swap just tracks position until the drag
// threshold is crossed.
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
		a.draw()
	case rightDragSwap:
		if !rd.started {
			dx := sx - rd.startX
			dy := sy - rd.startY
			if dx*dx+dy*dy >= dragThreshold*dragThreshold {
				rd.started = true
			}
		}
	}
}

// paneRectToRect converts the wasm-local paneRect to the pane.Rect
// type expected by pane.RatioFromCursor.
func paneRectToRect(r paneRect) pane.Rect {
	return pane.Rect{X: r.X, Y: r.Y, W: r.W, H: r.H}
}

// finishRightDrag commits the pending right-button gesture at the
// current cursor position and clears the drag state. (sx, sy) are the
// release coordinates.
func (a *App) finishRightDrag(sx, sy float64) {
	rd := a.rightDrag
	if rd == nil {
		return
	}
	a.rightDrag = nil

	switch rd.kind {
	case rightDragResize:
		// Apply the final ratio so we can size up the resulting panes.
		newRatio := pane.RatioFromCursor(paneRectToRect(rd.container), rd.splitDir, sx, sy)
		rd.targetSplit.Ratio = newRatio
		// Decide if a side has been squeezed enough to commit close.
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
	case rightDragSwap:
		if rd.started {
			// True drag → swap. Identify destination pane from cursor.
			destPane, _, ok := a.paneAtScreen(sx, sy)
			if ok && destPane.ID != rd.originPaneID {
				_ = a.tree.Swap(rd.originPaneID, destPane.ID)
				_ = a.tree.SetFocus(rd.originPaneID)
			}
		} else {
			// Bare right-click → split on the closest edge.
			a.startSplitOnEdge(rd.originPaneID, sx, sy)
		}
	}
	a.draw()
	a.scheduleURLUpdate()
}

// startSplitOnEdge reads the click position relative to the pane's
// rectangle, picks the closest edge, splits the tree on that side, and
// kicks off a "new pane growing out of the edge" animation.
func (a *App) startSplitOnEdge(paneID string, sx, sy float64) {
	p := a.tree.FindPane(paneID)
	if p == nil {
		return
	}
	r := paneRectFor(a, p)
	if r.W <= 0 || r.H <= 0 {
		return
	}
	pscreen := dragdrop.Pane{
		ScreenX: r.X, ScreenY: r.Y, ScreenW: r.W, ScreenH: r.H,
		Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom, CellPx: cellPx,
	}
	side := dragdrop.ClosestEdge(pscreen, sx, sy)
	paneSide := paneSideFromDragdrop(side)
	newP, err := a.tree.SplitOnSide(paneSide)
	if err != nil || newP == nil {
		return
	}
	parent := findParentSplitForPane(a.tree, newP.ID)
	if parent == nil {
		// Shouldn't happen — Split just created one — but bail safely.
		return
	}
	// New pane is in slot A iff the side asked for top/left.
	newOnA := paneSide == pane.SideTop || paneSide == pane.SideLeft
	from := 0.0
	if !newOnA {
		from = 1.0
	}
	parent.Ratio = from
	a.splitAnim = &splitGrowAnimation{
		target:     parent,
		fromRatio:  from,
		toRatio:    0.5,
		startMs:    nowMs(),
		durationMs: splitGrowMs,
	}
	a.scheduleFrame()
}

// paneSideFromDragdrop converts a dragdrop.Side to a pane.Side. Both
// enums use the same semantics; we keep them separate so dragdrop
// doesn't depend on pane.
func paneSideFromDragdrop(s dragdrop.Side) pane.Side {
	switch s {
	case dragdrop.SideTop:
		return pane.SideTop
	case dragdrop.SideBottom:
		return pane.SideBottom
	case dragdrop.SideLeft:
		return pane.SideLeft
	case dragdrop.SideRight:
		return pane.SideRight
	}
	return pane.SideTop
}

// findParentSplitForPane locates the *Split whose direct child is the
// pane with id `paneID`. Used by the split-grow animation to address
// the just-created split for ratio interpolation.
func findParentSplitForPane(t *pane.Tree, paneID string) *pane.Split {
	var found *pane.Split
	var walk func(n pane.Node)
	walk = func(n pane.Node) {
		if n.IsLeaf() || found != nil {
			return
		}
		if (n.Split.A.IsLeaf() && n.Split.A.Pane.ID == paneID) ||
			(n.Split.B.IsLeaf() && n.Split.B.Pane.ID == paneID) {
			found = n.Split
			return
		}
		walk(n.Split.A)
		walk(n.Split.B)
	}
	walk(t.Root)
	return found
}
