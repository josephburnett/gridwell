//go:build js && wasm

package main

import (
	"fmt"
	"math"
	"strings"
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
		nodeID:       0, // will be set below if click began on a node
		startScreenX: sx,
		startScreenY: sy,
		curScreenX:   sx,
		curScreenY:   sy,
		clone:        clone,
	}
	if n != nil {
		a.dragging.nodeID = n.ID
		// Save offset between cursor and node origin in cells, so the
		// drop position keeps the same grab point.
		ps := dragdrop.Pane{
			ScreenX: r.X, ScreenY: r.Y, ScreenW: r.W, ScreenH: r.H,
			Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom, CellPx: cellPx,
		}
		cx, cy := ps.ScreenToCell(sx, sy)
		a.dragging.cellOffsetX = cx - float64(n.X)
		a.dragging.cellOffsetY = cy - float64(n.Y)
	}
	return nil
}

func (a *App) onMouseMove(this js.Value, args []js.Value) any {
	sx, sy := mouseXY(args[0], a.canvas)
	// If the menu is open, track hover for highlighting.
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
	// Promote to "started" once we exceed the threshold.
	if !d.started {
		dxs := sx - d.startScreenX
		dys := sy - d.startScreenY
		if dxs*dxs+dys*dys >= dragThreshold*dragThreshold {
			d.started = true
		} else {
			return nil
		}
	}
	if d.nodeID == 0 {
		// Pan the focused pane smoothly.
		focused := a.tree.FindPane(d.originPaneID)
		if focused != nil {
			cellSize := cellPx * focused.Zoom
			focused.Cx -= (sx - d.curScreenX) / cellSize
			focused.Cy -= (sy - d.curScreenY) / cellSize
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

	// Node move/clone drag. Resolve drop coordinates in the pane the cursor
	// is currently over.
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
				NodeID:     d.nodeID,
				DestGridID: dstGridID, DestPath: rpc.Path{WellIDs: destPane.Path}, DestViewRect: dstView,
				X: dropX, Y: dropY,
			}
			var resp rpc.NodeResponse
			_, _ = postJSON("/rpc/CloneNode", req, &resp)
		} else {
			req := rpc.MoveNodeRequest{
				Path: rpc.Path{WellIDs: srcPane.Path}, ViewRect: srcView,
				NodeID:     d.nodeID,
				DestGridID: dstGridID, DestPath: rpc.Path{WellIDs: destPane.Path}, DestViewRect: dstView,
				X: dropX, Y: dropY,
			}
			var resp rpc.MoveNodeResponse
			_, _ = postJSON("/rpc/MoveNode", req, &resp)
		}
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

// onContextMenu (right click) descends into the well under the cursor.
// On empty space or files (not yet supported for descent), it does nothing.
func (a *App) onContextMenu(this js.Value, args []js.Value) any {
	args[0].Call("preventDefault")
	sx, sy := mouseXY(args[0], a.canvas)
	p, r, ok := a.paneAtScreen(sx, sy)
	if !ok {
		return nil
	}
	_ = a.tree.SetFocus(p.ID)
	cellX, cellY := cellAtScreen(p, r, sx, sy)
	hit := a.nodeAtCell(p, cellX, cellY)
	if hit == nil || hit.Type != "well" || hit.Capped {
		return nil
	}
	p.Path = append(append([]int64(nil), p.Path...), hit.ID)
	p.Cx, p.Cy = 0, 0
	delete(a.selectedNodeID, p.ID)
	a.fetchGrid(hit.ChildGridID)
	a.draw()
	a.saveTreeToLocalStorage()
	return nil
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
		if a.menuOpen {
			a.menuOpen = false
			a.draw()
			return nil
		}
		if len(focused.Path) > 0 {
			focused.Path = focused.Path[:len(focused.Path)-1]
			focused.Cx, focused.Cy = 0, 0
			delete(a.selectedNodeID, focused.ID)
		} else {
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

// silence unused.
var _ = fmt.Sprintf
