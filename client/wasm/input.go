//go:build js && wasm

package main

import (
	"syscall/js"

	"github.com/josephburnett/ascent/client/dragdrop"
	"github.com/josephburnett/ascent/client/pane"
	"github.com/josephburnett/ascent/internal/rpc"
)

// installCanvasInput attaches mouse and keyboard listeners to the canvas.
func (a *App) installCanvasInput() {
	a.canvas.Call("addEventListener", "wheel", js.FuncOf(a.onWheel))
	a.canvas.Call("addEventListener", "mousedown", js.FuncOf(a.onMouseDown))
	a.canvas.Call("addEventListener", "mousemove", js.FuncOf(a.onMouseMove))
	a.canvas.Call("addEventListener", "mouseup", js.FuncOf(a.onMouseUp))
	a.canvas.Call("addEventListener", "contextmenu", js.FuncOf(a.onContextMenu))
	a.canvas.Call("addEventListener", "dblclick", js.FuncOf(a.onDoubleClick))
	a.canvas.Call("addEventListener", "keydown", js.FuncOf(a.onKeyDown))
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
	factor := zoomFactor
	if dy > 0 {
		factor = 1 / zoomFactor
	}
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

	pscreen := dragdrop.Pane{
		ScreenX: r.X, ScreenY: r.Y, ScreenW: r.W, ScreenH: r.H,
		Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom, CellPx: cellPx,
	}
	cx, cy := pscreen.ScreenToCell(sx, sy)
	cellX, cellY := dragdrop.SnapToCell(cx), dragdrop.SnapToCell(cy)

	button := args[0].Get("button").Int()
	if button != 0 {
		// Right click handled by onContextMenu.
		return nil
	}

	n := a.nodeAtCell(p, cellX, cellY)
	if n == nil {
		// Begin a pan drag.
		a.dragging = &dragState{
			originPaneID: p.ID,
			nodeID:       0,
			cellOffsetX:  sx,
			cellOffsetY:  sy,
			curScreenX:   sx,
			curScreenY:   sy,
		}
		return nil
	}
	// Begin a node drag.
	clone := args[0].Get("altKey").Bool()
	a.dragging = &dragState{
		originPaneID: p.ID,
		nodeID:       n.ID,
		cellOffsetX:  cx - float64(n.X),
		cellOffsetY:  cy - float64(n.Y),
		curScreenX:   sx,
		curScreenY:   sy,
		clone:        clone,
	}
	a.draw()
	return nil
}

func (a *App) onMouseMove(this js.Value, args []js.Value) any {
	sx, sy := mouseXY(args[0], a.canvas)
	if a.dragging == nil {
		return nil
	}
	if a.dragging.nodeID == 0 {
		// Pan: move the focused pane's viewport.
		p, _, ok := a.paneAtScreen(a.dragging.curScreenX, a.dragging.curScreenY)
		if ok {
			cellSize := cellPx * p.Zoom
			p.Cx -= (sx - a.dragging.curScreenX) / cellSize
			p.Cy -= (sy - a.dragging.curScreenY) / cellSize
		}
	}
	a.dragging.curScreenX = sx
	a.dragging.curScreenY = sy
	a.draw()
	return nil
}

func (a *App) onMouseUp(this js.Value, args []js.Value) any {
	if a.dragging == nil {
		return nil
	}
	d := a.dragging
	a.dragging = nil

	if d.nodeID == 0 {
		// End of pan drag. Save state.
		a.saveTreeToLocalStorage()
		a.draw()
		return nil
	}

	sx, sy := mouseXY(args[0], a.canvas)
	destPane, destRect, ok := a.paneAtScreen(sx, sy)
	if !ok {
		a.draw()
		return nil
	}
	srcPane := a.tree.FindPane(d.originPaneID)
	if srcPane == nil {
		a.draw()
		return nil
	}
	dpscreen := dragdrop.Pane{
		ScreenX: destRect.X, ScreenY: destRect.Y, ScreenW: destRect.W, ScreenH: destRect.H,
		Cx: destPane.Cx, Cy: destPane.Cy, Zoom: destPane.Zoom, CellPx: cellPx,
	}
	dcx, dcy := dpscreen.ScreenToCell(sx, sy)
	dropX := dragdrop.SnapToCell(dcx - d.cellOffsetX)
	dropY := dragdrop.SnapToCell(dcy - d.cellOffsetY)

	srcRect := paneRectFor(a, srcPane)
	srcView := a.paneViewRect(srcPane, dragdrop.Pane{
		ScreenX: srcRect.X, ScreenY: srcRect.Y, ScreenW: srcRect.W, ScreenH: srcRect.H,
		Cx: srcPane.Cx, Cy: srcPane.Cy, Zoom: srcPane.Zoom, CellPx: cellPx,
	})
	dstView := a.paneViewRect(destPane, dpscreen)

	dstGridID := a.gridIDForPath(destPane.Path)

	go func() {
		if d.clone {
			req := rpc.CloneNodeRequest{
				Path: rpc.Path{WellIDs: srcPane.Path}, ViewRect: srcView,
				NodeID: d.nodeID,
				DestGridID: dstGridID, DestPath: rpc.Path{WellIDs: destPane.Path}, DestViewRect: dstView,
				X: dropX, Y: dropY,
			}
			var resp rpc.NodeResponse
			_, _ = postJSON("/rpc/CloneNode", req, &resp)
		} else {
			req := rpc.MoveNodeRequest{
				Path: rpc.Path{WellIDs: srcPane.Path}, ViewRect: srcView,
				NodeID: d.nodeID,
				DestGridID: dstGridID, DestPath: rpc.Path{WellIDs: destPane.Path}, DestViewRect: dstView,
				X: dropX, Y: dropY,
			}
			var resp rpc.MoveNodeResponse
			_, _ = postJSON("/rpc/MoveNode", req, &resp)
		}
		// Refetch affected grids; events will redraw too.
		a.fetchGrid(a.gridIDForPath(srcPane.Path))
		a.fetchGrid(dstGridID)
	}()
	a.draw()
	return nil
}

func paneRectFor(a *App, p *pane.Pane) paneRect {
	rects := a.layoutPanes()
	if r, ok := rects[p.ID]; ok {
		return r
	}
	return paneRect{}
}

func (a *App) onContextMenu(this js.Value, args []js.Value) any {
	args[0].Call("preventDefault")
	sx, sy := mouseXY(args[0], a.canvas)
	p, r, ok := a.paneAtScreen(sx, sy)
	if !ok {
		return nil
	}
	pscreen := dragdrop.Pane{
		ScreenX: r.X, ScreenY: r.Y, ScreenW: r.W, ScreenH: r.H,
		Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom, CellPx: cellPx,
	}
	cx, cy := pscreen.ScreenToCell(sx, sy)
	cellX, cellY := dragdrop.SnapToCell(cx), dragdrop.SnapToCell(cy)
	if hit := a.nodeAtCell(p, cellX, cellY); hit != nil {
		// On a node: simple action — cap/redig if well, otherwise no-op
		// for v1 (a richer menu is a future enhancement).
		go a.toggleCap(p, hit)
		return nil
	}
	// Empty cell: create a 1x1 well.
	view := a.paneViewRect(p, pscreen)
	gid := a.gridIDForPath(p.Path)
	go func() {
		req := rpc.CreateWellRequest{
			Path: rpc.Path{WellIDs: p.Path}, ViewRect: view,
			GridID: gid, X: cellX, Y: cellY, W: 1, H: 1,
		}
		var resp rpc.NodeResponse
		_, _ = postJSON("/rpc/CreateWell", req, &resp)
		a.fetchGrid(gid)
	}()
	return nil
}

func (a *App) toggleCap(p *pane.Pane, n *rpc.Node) {
	if n.Type != "well" {
		return
	}
	srcRect := paneRectFor(a, p)
	pscreen := dragdrop.Pane{
		ScreenX: srcRect.X, ScreenY: srcRect.Y, ScreenW: srcRect.W, ScreenH: srcRect.H,
		Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom, CellPx: cellPx,
	}
	view := a.paneViewRect(p, pscreen)
	method := "/rpc/CapWell"
	if n.Capped {
		method = "/rpc/RedigWell"
	}
	req := rpc.CapWellRequest{
		Path: rpc.Path{WellIDs: p.Path}, ViewRect: view, NodeID: n.ID,
	}
	var resp rpc.NodeResponse
	_, _ = postJSON(method, req, &resp)
	a.fetchGrid(a.gridIDForPath(p.Path))
}

// onDoubleClick on a well descends; on empty space inside an already-deep
// pane, ascends. (Esc keyboard shortcut also ascends.)
func (a *App) onDoubleClick(this js.Value, args []js.Value) any {
	sx, sy := mouseXY(args[0], a.canvas)
	p, r, ok := a.paneAtScreen(sx, sy)
	if !ok {
		return nil
	}
	pscreen := dragdrop.Pane{
		ScreenX: r.X, ScreenY: r.Y, ScreenW: r.W, ScreenH: r.H,
		Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom, CellPx: cellPx,
	}
	cx, cy := pscreen.ScreenToCell(sx, sy)
	cellX, cellY := dragdrop.SnapToCell(cx), dragdrop.SnapToCell(cy)
	hit := a.nodeAtCell(p, cellX, cellY)
	if hit != nil && hit.Type == "well" && !hit.Capped {
		p.Path = append(append([]int64(nil), p.Path...), hit.ID)
		p.Cx, p.Cy = 0, 0
		a.fetchGrid(hit.ChildGridID)
		a.draw()
		a.saveTreeToLocalStorage()
		return nil
	}
	return nil
}

func (a *App) onKeyDown(this js.Value, args []js.Value) any {
	key := args[0].Get("key").String()
	focused := a.tree.FocusedPane()
	if focused == nil {
		return nil
	}
	step := 1.0 / focused.Zoom
	switch key {
	case "ArrowLeft", "a", "A":
		focused.Cx -= step
	case "ArrowRight", "d", "D":
		focused.Cx += step
	case "ArrowUp", "w", "W":
		focused.Cy -= step
	case "ArrowDown", "s", "S":
		focused.Cy += step
	case "Escape":
		if len(focused.Path) > 0 {
			focused.Path = focused.Path[:len(focused.Path)-1]
			focused.Cx, focused.Cy = 0, 0
		} else {
			// Ascent at root.
			go func() {
				var resp rpc.AscendAtRootResponse
				if _, err := postJSON("/rpc/AscendAtRoot", rpc.AscendAtRootRequest{}, &resp); err == nil {
					a.user.RootGridID = resp.NewRootGridID
					a.fetchGrid(resp.NewRootGridID)
				}
			}()
		}
	case "+", "=":
		focused.Zoom *= zoomFactor
	case "-", "_":
		focused.Zoom /= zoomFactor
	default:
		return nil
	}
	if focused.Zoom < zoomMin {
		focused.Zoom = zoomMin
	}
	if focused.Zoom > zoomMax {
		focused.Zoom = zoomMax
	}
	args[0].Call("preventDefault")
	a.draw()
	a.saveTreeToLocalStorage()
	return nil
}

// mouseXY returns the click coordinates relative to the canvas.
func mouseXY(ev js.Value, canvas js.Value) (float64, float64) {
	rect := canvas.Call("getBoundingClientRect")
	x := ev.Get("clientX").Float() - rect.Get("left").Float()
	y := ev.Get("clientY").Float() - rect.Get("top").Float()
	return x, y
}
