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
)

// installCanvasInput attaches mouse listeners to the canvas. Ascent is
// mouse-only by design — every gesture has a pointer equivalent and the
// keyboard is reserved for future text-editing modes (e.g., the markdown
// editor that's still TODO).
func (a *App) installCanvasInput() {
	a.canvas.Call("addEventListener", "wheel", js.FuncOf(a.onWheel))
	a.canvas.Call("addEventListener", "mousedown", js.FuncOf(a.onMouseDown))
	a.canvas.Call("addEventListener", "mousemove", js.FuncOf(a.onMouseMove))
	a.canvas.Call("addEventListener", "mouseup", js.FuncOf(a.onMouseUp))
	a.canvas.Call("addEventListener", "contextmenu", js.FuncOf(a.onContextMenu))
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
	a.saveTreeToLocalStorage()
	return nil
}

func (a *App) onMouseDown(this js.Value, args []js.Value) any {
	sx, sy := mouseXY(args[0], a.canvas)
	p, r, ok := a.paneAtScreen(sx, sy)
	if !ok {
		return nil
	}
	_ = a.tree.SetFocus(p.ID)
	button := args[0].Get("button").Int()
	if button != 0 {
		// Right click handled by onContextMenu.
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
		// Pan the source pane smoothly.
		focused := a.tree.FindPane(d.originPaneID)
		if focused != nil {
			cellSize := cellPx * focused.Zoom
			focused.Cx -= (sx - d.curScreenX) / cellSize
			focused.Cy -= (sy - d.curScreenY) / cellSize
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
	if a.dragging == nil {
		return nil
	}
	d := a.dragging
	a.dragging = nil
	sx, sy := mouseXY(args[0], a.canvas)

	// Bare click (no movement): selection.
	if !d.started {
		focused := a.tree.FindPane(d.originPaneID)
		if focused == nil {
			a.draw()
			return nil
		}
		// Use the pane rect as it is now (still the same — no resize).
		r := paneRectFor(a, focused)
		cellX, cellY := cellAtScreen(focused, r, sx, sy)
		if n := a.nodeAtCell(focused, cellX, cellY); n != nil {
			a.selectedNodeID[focused.ID] = n.ID
		} else {
			delete(a.selectedNodeID, focused.ID)
		}
		a.draw()
		a.saveTreeToLocalStorage()
		return nil
	}

	// Pan drag end: just persist viewport state.
	if d.nodeID == 0 {
		a.saveTreeToLocalStorage()
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

// onContextMenu (right click) drives mouse-only navigation:
//   - On a well in the inner area: smooth descent into the well.
//   - In the edge band of the pane: smooth ascent (or AscendAtRoot at top).
//   - Otherwise: no-op.
//
// Edge takes priority over node hits — the user explicitly wants the edge
// to always ascend so they always have a reachable ascent target.
func (a *App) onContextMenu(this js.Value, args []js.Value) any {
	args[0].Call("preventDefault")
	sx, sy := mouseXY(args[0], a.canvas)
	p, r, ok := a.paneAtScreen(sx, sy)
	if !ok {
		return nil
	}
	_ = a.tree.SetFocus(p.ID)
	pscreen := dragdrop.Pane{
		ScreenX: r.X, ScreenY: r.Y, ScreenW: r.W, ScreenH: r.H,
		Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom, CellPx: cellPx,
	}
	if dragdrop.IsInEdgeZone(pscreen, sx, sy, dragdrop.EdgeBand(pscreen)) {
		if len(p.Path) > 0 {
			a.startAscent(p)
		} else {
			go func() {
				var resp rpc.AscendAtRootResponse
				if _, err := postJSON("/rpc/AscendAtRoot", rpc.AscendAtRootRequest{}, &resp); err == nil {
					a.user.RootGridID = resp.NewRootGridID
					a.fetchGrid(resp.NewRootGridID)
				}
			}()
		}
		return nil
	}
	cellX, cellY := cellAtScreen(p, r, sx, sy)
	hit := a.nodeAtCell(p, cellX, cellY)
	if hit == nil || hit.Type != "well" || hit.Capped {
		return nil
	}
	a.startDescent(p, hit)
	return nil
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

// startAscent installs a multi-segment transition that smoothly zooms out
// of the child grid back to the parent grid and then un-pans to the
// viewport the user was at before they descended (popped from
// paneStateStack). The motion is serialized: zoom first, pan second, so
// the camera retraces the descent's path-then-zoom in reverse.
//
// Phases:
//   A. (child) From current state → calibrated switch state. Combined
//      pan+zoom because the user may have moved in the child grid.
//   B. (parent, post-switch) Zoom out at well center from "well overtakes
//      view" to the saved zoom.
//   C. (parent) Pan from well center to the saved (Cx, Cy) at saved zoom.
//
// If no saved state is available (cleared localStorage, reload), we land
// on the well at zoom 1.
func (a *App) startAscent(p *pane.Pane) {
	if len(p.Path) == 0 {
		return
	}
	wellID := p.Path[len(p.Path)-1]
	parentPath := append([]int64(nil), p.Path[:len(p.Path)-1]...)
	parentGridID := a.gridIDForPath(parentPath)
	parentGrid, ok := a.c.Grid(parentGridID)
	if !ok {
		a.fetchGrid(parentGridID)
		a.instantAscend(p, parentPath)
		return
	}
	well, ok := parentGrid.Nodes[wellID]
	if !ok {
		a.instantAscend(p, parentPath)
		return
	}
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
	// time uniformly.
	childDist := panDist(mid.Cx-from.Cx, mid.Cy-from.Cy, from.Zoom) +
		zoomDist(from.Zoom, mid.Zoom)
	parentZoomDist := zoomDist(switchTo.Zoom, saved.Zoom)
	parentPanDist := panDist(saved.Cx-switchTo.Cx, saved.Cy-switchTo.Cy, saved.Zoom)
	durations := anim.SplitN([]float64{childDist, parentZoomDist, parentPanDist}, totalTransitionMs)

	a.startTransition(&paneTransition{
		paneID: p.ID,
		segments: []transSegment{
			// Phase A: combined pan+zoom in child to land on calibrated state.
			{
				path:   from.Path,
				fromCx: from.Cx, fromCy: from.Cy, fromZoom: from.Zoom,
				toCx: mid.Cx, toCy: mid.Cy, toZoom: mid.Zoom,
				durationMs: durations[0],
			},
			// Phase B: switch to parent; zoom out at well center.
			{
				path:   switchTo.Path,
				fromCx: switchTo.Cx, fromCy: switchTo.Cy, fromZoom: switchTo.Zoom,
				toCx: switchTo.Cx, toCy: switchTo.Cy, toZoom: saved.Zoom,
				durationMs: durations[1],
			},
			// Phase C: pan from well center back to saved at saved zoom.
			{
				path:   switchTo.Path,
				fromCx: switchTo.Cx, fromCy: switchTo.Cy, fromZoom: saved.Zoom,
				toCx: saved.Cx, toCy: saved.Cy, toZoom: saved.Zoom,
				durationMs: durations[2],
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
	a.saveTreeToLocalStorage()
}

// startDescent pushes the pane's current state onto the saved-state stack
// and installs a serialized pan-then-zoom transition into the well's
// child grid. Splitting the motion (rather than doing it together) avoids
// the "whiplash" of a simultaneous diagonal zoom.
//
// Phases:
//   A. Pan in parent at constant Zoom from current to well center.
//   B. Zoom in parent at well center from current zoom to overtake.
//   C. Instant install of the calibrated child state.
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

	pDist := panDist(mid.Cx-from.Cx, mid.Cy-from.Cy, from.Zoom)
	zDist := zoomDist(from.Zoom, mid.Zoom)
	panMs, zoomMs := anim.SplitDuration(pDist, zDist, totalTransitionMs)

	a.startTransition(&paneTransition{
		paneID: p.ID,
		segments: []transSegment{
			// Phase A: pan to well at constant zoom.
			{
				path:   from.Path,
				fromCx: from.Cx, fromCy: from.Cy, fromZoom: from.Zoom,
				toCx: mid.Cx, toCy: mid.Cy, toZoom: from.Zoom,
				durationMs: panMs,
			},
			// Phase B: zoom in at well center.
			{
				path:   from.Path,
				fromCx: mid.Cx, fromCy: mid.Cy, fromZoom: from.Zoom,
				toCx: mid.Cx, toCy: mid.Cy, toZoom: mid.Zoom,
				durationMs: zoomMs,
			},
			// Phase C: instant land at calibrated child state.
			{
				path:   to.Path,
				fromCx: to.Cx, fromCy: to.Cy, fromZoom: to.Zoom,
				toCx: to.Cx, toCy: to.Cy, toZoom: to.Zoom,
				durationMs: 0,
			},
		},
	})
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
