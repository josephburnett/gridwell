//go:build js && wasm

package main

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/anim"
	"github.com/josephburnett/gridwell/client/dragdrop"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/gesture"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/panebox"
	"github.com/josephburnett/gridwell/client/traceevent"
	"github.com/josephburnett/gridwell/client/wsbar"
	"github.com/josephburnett/gridwell/client/zoomtrans"
)

// The canvas pointer event flow, in the order the events arrive. It gathers the
// impure facts and hands them to the pure deciders in client/gesture,
// client/dragdrop, client/zoomtrans and client/pane.

// installCanvasInput attaches presses and the wheel on the canvas, where a
// gesture can only start, and move and release at the window. Navigation is
// mouse-only: there are no navigation keybindings.
func (a *App) installCanvasInput() {
	a.canvas.Call("addEventListener", "wheel", js.FuncOf(a.onWheel))
	a.canvas.Call("addEventListener", "mousedown", js.FuncOf(a.onMouseDown))
	// At the window, in the capture phase, so a gesture keeps tracking whatever
	// the pointer crosses: a canvas listener would hear neither once a fast drag
	// jumped into a DOM overlay, and capture beats an overlay's stopPropagation.
	// With no gesture armed, non-canvas events are ignored.
	captureOpts := js.ValueOf(map[string]any{"capture": true})
	a.win.Call("addEventListener", "mousemove", js.FuncOf(func(this js.Value, args []js.Value) any {
		if !a.gestureInFlight() && !args[0].Get("target").Equal(a.canvas) {
			return nil
		}
		return a.onMouseMove(this, args)
	}), captureOpts)
	a.win.Call("addEventListener", "mouseup", js.FuncOf(func(this js.Value, args []js.Value) any {
		if !a.gestureInFlight() && !args[0].Get("target").Equal(a.canvas) {
			return nil
		}
		return a.onMouseUp(this, args)
	}), captureOpts)
	// Right-click is the clone/resize gesture stem, not a menu.
	a.canvas.Call("addEventListener", "contextmenu", js.FuncOf(func(this js.Value, args []js.Value) any {
		args[0].Call("preventDefault")
		return nil
	}))
	// Window-level so the content-zoom chord is caught wherever focus sits.
	a.win.Call("addEventListener", "keydown", js.FuncOf(a.onKeyDown))
	// In the capture phase, ahead of every overlay's own Esc: a drag in flight
	// owns the key wherever DOM focus sits. With none, Esc goes on as before.
	a.win.Call("addEventListener", "keydown", js.FuncOf(func(this js.Value, args []js.Value) any {
		if args[0].Get("key").String() == "Escape" && a.cancelGesture(false) {
			args[0].Call("preventDefault")
			args[0].Call("stopPropagation")
		}
		return nil
	}), captureOpts)
	// Single-finger touch becomes the same mouse gestures; see touch.go.
	a.installTouchInput()
	a.installFocusTrace()
}

// onKeyDown owns the content-zoom chord. Esc on a drag in flight is
// cancelGesture's; overlays own every other key.
func (a *App) onKeyDown(_ js.Value, args []js.Value) any {
	if len(args) == 0 {
		return nil
	}
	// Checked first so Electron's built-in page zoom never double-fires.
	a.handleContentZoomKey(args[0])
	return nil
}

// gestureInFlight is the routing gate for the window-level move and up.
func (a *App) gestureInFlight() bool {
	return a.leftResize != nil || a.rightDrag != nil || a.dragging != nil
}

// armed reports the three gesture states to the verdicts in client/gesture.
func (a *App) armed() gesture.Armed {
	d := a.dragging
	return gesture.Armed{
		LeftResize:  a.leftResize != nil,
		RightDrag:   a.rightDrag != nil,
		Drag:        d != nil,
		DragCreates: d != nil && d.intent.Creates(),
		Pan:         d != nil && d.tileID == "" && !d.isTemplate,
	}
}

// cancelGesture lets go of every armed gesture and puts back what
// gesture.Escape names; false when nothing is armed. relayed is a key a parked
// live view handed on.
func (a *App) cancelGesture(relayed bool) bool {
	u, ok := gesture.Escape(a.armed())
	if !ok {
		return false
	}
	lr, d := a.leftResize, a.dragging
	a.leftResize, a.rightDrag, a.dragging = nil, nil, nil
	// Every press focuses its pane before it arms anything.
	a.emit(traceevent.Cancel(a.tree.Focus, relayed))
	if u.Dividers {
		lr.press.Restore()
	}
	if u.View {
		if p := a.tree.FindPane(d.originPaneID); p != nil {
			p.View = d.pressView
		}
	}
	a.canvas.Get("style").Set("cursor", "")
	if u.Ghost {
		a.cancelDragSnapBack(d)
	}
	a.draw()
	return true
}

// recoverLostRelease runs the commit path gesture.RecoverRelease names.
func (a *App) recoverLostRelease(buttons int, sx, sy float64) bool {
	switch gesture.RecoverRelease(buttons, a.armed()) {
	case gesture.FinishLeftResize:
		// From the last applied cursor, never this stray re-entry point.
		a.finishLeftResize()
		return true
	case gesture.FinishRightDrag:
		a.finishRightDrag(sx, sy)
		return true
	case gesture.FinishLeftDrag:
		return a.finishLeftDrag(sx, sy)
	}
	return false
}

// paneIDAt is the pane a press or release record names: the one under the
// pointer, or none over the bar and the strip.
func (a *App) paneIDAt(sx, sy float64) string {
	if p, _, ok := a.paneAtScreen(sx, sy); ok && p != nil {
		return p.ID
	}
	return ""
}

func modsOf(ev js.Value) traceevent.Mods {
	return traceevent.Mods{Ctrl: ev.Get("ctrlKey").Truthy(), Shift: ev.Get("shiftKey").Truthy(),
		Alt: ev.Get("altKey").Truthy(), Meta: ev.Get("metaKey").Truthy()}
}

func (a *App) paneAtScreen(sx, sy float64) (*pane.Pane, pane.Rect, bool) {
	rects := a.layoutPanes()
	for id, r := range rects {
		if r.Contains(sx, sy) {
			return a.tree.FindPane(id), r, true
		}
	}
	return nil, pane.Rect{}, false
}

// menuPaneForPointer routes an open palette's pointer events to the menu's own
// pane, never the pane under the cursor: every swatch rect is laid out for the
// menu's pane, so routing by the pointer would move focus out of the menu.
func (a *App) menuPaneForPointer() (*pane.Pane, pane.Rect, bool) {
	mp := a.tree.FindPane(a.menu.PaneID()) // PaneID is "" while closed
	if mp == nil {
		return nil, pane.Rect{}, false
	}
	return mp, paneRectFor(a, mp), true
}

// cellAtScreen floors: round-half makes clicks in a tile's lower-right miss.
// false when p shows nothing at r.
func cellAtScreen(p *pane.Pane, r pane.Rect, sx, sy float64) (int64, int64, bool) {
	ps, ok := p.Screen(r)
	if !ok {
		return 0, 0, false
	}
	x, y := ps.CellAt(sx, sy)
	return x, y, true
}

func (a *App) tileAtCell(p *pane.Pane, cellX, cellY int64) *gridwellv1.Tile {
	gid := a.gridIDForPane(p)
	g, ok := a.c.Grid(gid)
	if !ok {
		return nil
	}
	for _, n := range g.Tiles {
		if dragdrop.TileContainsCell(n.X, n.Y, n.W, n.H, cellX, cellY) {
			nn := n
			return nn
		}
	}
	return nil
}

// focusToPane is the single focus-transfer owner; every press path calls it,
// and it is a no-op on the same pane. A real change tells menu.TransferFocus,
// which closes a menu left on the old pane.
func (a *App) focusToPane(p *pane.Pane) bool {
	prev := a.tree.Focus
	_ = a.tree.SetFocus(p.ID)
	if !a.menu.TransferFocus(prev, a.tree.Focus) {
		return false
	}
	a.emit(traceevent.Focus(prev, a.tree.Focus))
	// The textarea overlay only ever lives over the focused pane, so without
	// this a click on a sibling in text mode strands it.
	a.refreshFileOverlay()
	a.draw()
	return true
}

func (a *App) onWheel(this js.Value, args []js.Value) any {
	args[0].Call("preventDefault")
	dy := args[0].Get("deltaY").Float()
	sx, sy := mouseXY(args[0], a.canvas)
	// The band is under no pane, so a wheel there is classified for the
	// focused pane; gesture.ClassifyWheel owns what it does.
	if bx, top, bw, barOK := a.bottomBarRect(); barOK &&
		wsbar.Where(sx, sy, bx, top, bw) == wsbar.ZoneBar {
		fp := a.tree.FocusedPane()
		if fp == nil {
			return nil
		}
		if gesture.ClassifyWheel(gesture.WheelInput{OverBar: true, TextFocused: fp.ContentID() != ""}) == gesture.WheelZoomFocused {
			fr := a.paneRectByID(fp.ID)
			a.wheelZoomPaneAt(fp, fr, dy, fr.X+fr.W/2, fr.Y+fr.H/2)
		}
		return nil
	}
	p, r, ok := a.paneAtScreen(sx, sy)
	if !ok {
		return nil
	}
	// gesture.ClassifyWheel routes; this handler only resolves the impure facts.
	var hoverWell *gridwellv1.Tile
	wellCoverage := 0.0
	ps, shown := p.Screen(r)
	if shown && p.ContentID() == "" {
		if t := a.tileAtScreen(p, r, sx, sy); t != nil && rpc.IsWellKind(t.Kind) && t.ChildGridId != "" {
			hoverWell = t
			x0, y0 := ps.CellToScreen(float64(t.X), float64(t.Y))
			x1, _ := ps.CellToScreen(float64(t.X)+1, float64(t.Y))
			cell := x1 - x0
			b := panebox.ContentBox(r, paneBorderPx)
			wellCoverage = gesture.RectCoverage(
				x0, y0, float64(t.W)*cell, float64(t.H)*cell, b.X, b.Y, b.W, b.H)
		}
	}
	switch gesture.ClassifyWheel(gesture.WheelInput{
		TextFocused:       p.ContentID() != "",
		URLDescent:        a.isURLDescent(p),
		LiveURLView:       a.urlViewFor(p.ID) != nil,
		InContentBox:      pointInPaneContent(r, sx, sy),
		TextModeRendered:  p.TextMode == rpc.TextModeRendered,
		OverEnterableWell: hoverWell != nil,
		ZoomOut:           dy > 0,
		WellCoverage:      wellCoverage,
	}) {
	case gesture.WheelSwallow:
		// A live URL view scrolls itself; a stray wheel must not zoom the pane.
		args[0].Call("preventDefault")
		return nil
	case gesture.WheelScrollDoc:
		// A text tile has a fixed scale, so the wheel scrolls the rendered
		// window. In text mode the textarea scrolls itself.
		a.scrollText(p, p.TextScrollX, p.TextScrollY+dy)
		return nil
	case gesture.WheelIgnore:
		return nil
	case gesture.WheelZoomWell:
		// The wheel zooms the grid inside the hovered well, its stored preview
		// framing, not the grid the pane shows. The settle persister posts one
		// framing write per tile at flush.
		x0, y0 := ps.CellToScreen(float64(hoverWell.X), float64(hoverWell.Y))
		x1, _ := ps.CellToScreen(float64(hoverWell.X)+1, float64(hoverWell.Y))
		parentCell := x1 - x0
		wpx := float64(hoverWell.W) * parentCell
		hpx := float64(hoverWell.H) * parentCell
		zw := wellOf(hoverWell)
		// The float center accumulates across the burst: the first notch seeds
		// from the same Center the preview was drawn with, and later notches
		// feed the drift back in.
		cx0, cy0 := zw.Center()
		if st, ok := a.persist.wellWheelPending[hoverWell.Id]; ok {
			cx0, cy0 = st.cx, st.cy
		}
		cx1, cy1, ratio, changed := zoomtrans.WellWheelView(dy, zw, parentCell,
			sx-(x0+wpx/2), sy-(y0+hpx/2), cx0, cy0, zoomFactor, wellZoomRatioMin, wellZoomRatioMax)
		if !changed {
			return nil
		}
		a.c.PatchTile(hoverWell, func(t *gridwellv1.Tile) {
			t.ViewCx, t.ViewCy, t.ViewZoom = cx1, cy1, ratio
		})
		a.persist.wellWheelPending[hoverWell.Id] = wellWheelDrift{
			gridID: a.gridIDForPane(p), cx: cx1, cy: cy1,
			ratio: ratio, version: hoverWell.Version,
		}
		a.draw()
		return nil
	}
	// WheelZoomPane — smooth zoom centered on the cursor.
	a.wheelZoomPaneAt(p, r, dy, sx, sy)
	return nil
}

// scrollText is every scroll of a text descent: the wheel's and either
// overlay's own. See pane.Frame.ScrollText.
func (a *App) scrollText(p *pane.Pane, x, y float64) {
	if p.ScrollText(x, y) {
		a.draw()
	}
}

// wheelZoomPaneAt keeps the world point under (sx, sy) under it after the zoom,
// map-style. The bar-band wheel passes the pane's center.
func (a *App) wheelZoomPaneAt(p *pane.Pane, r pane.Rect, dy, sx, sy float64) {
	ps, ok := p.Screen(r)
	if !ok {
		return
	}
	v := ps.View()
	cellX, cellY := ps.ScreenToCell(sx, sy)
	if z, cx, cy, ok := zoomtrans.WheelZoom(dy, v.Zoom(), v.Cx(), v.Cy(), cellX, cellY, zoomFactor, zoomMin, zoomMax); ok {
		p.SetView(cx, cy, z)
	}
	a.draw()
	a.scheduleURLUpdate()
}

// grabTile records what the ghost and the commit read. The cursor offset is in
// the tile's own cell units, so the grab point tracks the cursor at any zoom,
// and every arm records the same six fields the same way.
func (d *dragState) grabTile(n *gridwellv1.Tile, cursorCellX, cursorCellY, tlX, tlY float64) {
	d.tileID = n.Id
	d.snapshotTile = n
	d.cellOffsetX = cursorCellX - float64(n.X)
	d.cellOffsetY = cursorCellY - float64(n.Y)
	d.originScreenX = tlX
	d.originScreenY = tlY
}

func (a *App) onMouseDown(this js.Value, args []js.Value) any {
	sx, sy := mouseXY(args[0], a.canvas)
	a.emit(traceevent.Press(a.paneIDAt(sx, sy), args[0].Get("button").Int(), modsOf(args[0]), false))
	// The tree and the viewport stay atomic across a transition.
	if a.trans.Any() {
		return nil
	}
	// A landing ghost parks every live surface, and its drop has already
	// committed, so a press ends the landing rather than missing a surface.
	if a.ghost != nil && a.dragging == nil && a.rightDrag == nil {
		a.ghost = nil
	}
	// The notice strip occupies the band layoutPanes reserved below every pane,
	// so a click there cannot be meant for a pane; errsurface owns the geometry.
	if stripH := errsurface.StripHeight(a.errs.Len()); stripH > 0 && sy >= a.height-stripH {
		if a.errs.DismissAt(sy, a.height-stripH) {
			a.draw()
		}
		return nil
	}
	// A left-click on a pane-tile crumb goes there, closing everything deeper,
	// the one gesture that crosses the level boundary; a right-click renames.
	if a.bottomBarClick(sx, sy, args[0].Get("button").Int()) {
		args[0].Call("preventDefault")
		return nil
	}
	// Routed before pane resolution: resolving the pane under the popover first
	// would close the very menu being used. Missing a swatch swallows the click.
	if mp, mr, ok := a.menuPaneForPointer(); ok && args[0].Get("button").Int() == 0 {
		if a.pointInPalette(mp, sx, sy) {
			// Not a swatch, so no drag arms; it changes only the menu.
			if a.pointInPaletteToggle(mp, sx, sy) {
				a.menu.TogglePlugins()
				a.draw()
				return nil
			}
			if idx := a.paletteTileIndexAt(mp, sx, sy); idx >= 0 {
				a.startPaletteDrag(mp, mr, idx, sx, sy)
			}
			return nil
		}
	}
	p, r, ok := a.paneAtScreen(sx, sy)
	if !ok {
		return nil
	}
	a.focusToPane(p)
	// Last, once the press has shown, un-parked or left what it acts on.
	defer a.takeKeyboard()
	button := args[0].Get("button").Int()
	if button == 2 {
		args[0].Call("preventDefault")
		// The modifier is read at the press and never again; see rightDragIntent.
		a.onRightDown(p, r, sx, sy, rightDragIntent(args[0]))
		return nil
	}
	if button == 1 {
		// The in-pane shortcut for the bar's crumb ascent. preventDefault
		// suppresses the browser's middle-click autoscroll.
		args[0].Call("preventDefault")
		a.menu.Close()
		a.ascendPane(p)
		return nil
	}
	if button != 0 {
		return nil
	}

	// Checked first, so a grab near the edge wins over content interactions.
	// preventDefault because native selection engages past the OS drag threshold
	// and steals the pointer; that steal is invisible to synthetic input, so the
	// e2e pins the prevented flag instead.
	if a.armLeftResize(r, sx, sy) {
		args[0].Call("preventDefault")
		return nil
	}

	// A surface that was not there to take the press gets it from here.
	if p.ContentID() != "" {
		// Held off so the canvas does not take the keyboard from the
		// surface the press lands on. An open rename is committed here, as
		// the canvas taking focus would have.
		args[0].Call("preventDefault")
		if in := a.doc.Call("getElementById", "gw-rename-input"); in.Truthy() {
			in.Call("blur")
		}
		if a.menu.OpenOn(p.ID) {
			a.menu.Close()
		}
		a.draw() // un-parks the surface the press lands on
		a.landPress(p, r, sx, sy, args[0])
		return nil
	}

	// A click inside the popover was claimed above, so this one is outside it.
	if a.menu.OpenOn(p.ID) {
		a.menu.Close()
		a.draw()
		// fall through so the click also pans / selects
	}

	ps, ok := p.Screen(r)
	if !ok {
		return nil
	}
	cellX, cellY := ps.CellAt(sx, sy)
	n := a.tileAtCell(p, cellX, cellY)
	parentCell := ps.Cell()
	a.dragging = &dragState{
		originPaneID: p.ID,
		splitNav:     args[0].Get("ctrlKey").Truthy(),
		tileID:       "",
		startScreenX: sx,
		startScreenY: sy,
		curScreenX:   sx,
		curScreenY:   sy,
		// Overridden below if the drag lands on a child preview tile.
		srcGridID:    a.gridIDForPane(p),
		srcCellSize:  parentCell,
		snapshotTile: &gridwellv1.Tile{},
		pressView:    p.View,
	}
	if n != nil {
		// Pull out of well: when a child preview tile sits at the cursor, that
		// is the drag source, not the well.
		if child := a.childTileAtScreen(p, r, n, sx, sy); child != nil {
			cp := wellPreviewFor(ps, n)
			cxF, cyF := cp.ChildCellAtScreen(sx, sy)
			tlX, tlY := cp.CellToScreen(float64(child.X), float64(child.Y))
			a.dragging.grabTile(child, cxF, cyF, tlX, tlY)
			a.dragging.srcGridID = n.ChildGridId
			a.dragging.srcCellSize = cp.CellPx
			return nil
		}
		cx, cy := ps.ScreenToCell(sx, sy)
		tlX, tlY := ps.CellToScreen(float64(n.X), float64(n.Y))
		a.dragging.grabTile(n, cx, cy, tlX, tlY)
	}
	return nil
}

func (a *App) onMouseMove(this js.Value, args []js.Value) any {
	sx, sy := mouseXY(args[0], a.canvas)
	// One owner for all three armed states.
	if a.recoverLostRelease(args[0].Get("buttons").Int(), sx, sy) {
		return nil
	}
	if a.leftResize != nil {
		a.onLeftResizeMove(sx, sy)
		return nil
	}
	// A move over a live URL view's content box belongs to the page. An in-flight
	// drag parks every live view, so liveViewOwnsPoint answers false then.
	if a.rightDrag == nil && a.dragging == nil {
		if p, r, ok := a.paneAtScreen(sx, sy); ok && a.liveViewOwnsPoint(p, r, sx, sy) {
			// The live view owns its own cursor.
			a.canvas.Get("style").Set("cursor", "")
		} else {
			// The canvas owns this point: a resize cursor over a grabbable
			// split divider, whose band is far wider than the 1px line.
			a.canvas.Get("style").Set("cursor", a.dividerResizeCursor(sx, sy))
		}
	}
	// Right-button gestures take precedence over the left paths below.
	if a.rightDrag != nil {
		a.onRightMove(sx, sy)
		return nil
	}
	// Palette hover routes through the same resolver the press uses, because
	// the swatches are laid out for the menu's own pane.
	if a.menu.IsOpen() {
		hover := -1
		if mp, _, ok := a.menuPaneForPointer(); ok {
			hover = a.paletteTileIndexAt(mp, sx, sy)
		}
		if a.menu.SetHover(hover) {
			a.draw()
		}
	}
	if a.dragging == nil {
		return nil
	}
	d := a.dragging
	// The one threshold, shared with the right-button clone drag.
	if !a.advanceDragGhost(d, sx, sy) {
		return nil
	}
	if d.tileID == "" && !d.isTemplate {
		// A pan drag only arms in grid mode, since a content descent swallows
		// the mousedown, so there is no text-scroll arm here.
		focused := a.tree.FindPane(d.originPaneID)
		if focused != nil {
			if v, ok := focused.Live(); ok {
				cellSize := cellPx * v.Zoom()
				focused.SetView(v.Cx()-(sx-d.curScreenX)/cellSize, v.Cy()-(sy-d.curScreenY)/cellSize, v.Zoom())
			}
		}
	} else if a.ghost != nil {
		// The same dragdrop.DecideDrop verdict onMouseUp commits, so a
		// previewed action cannot differ from the committed one.
		a.previewDrop(d, sx, sy)
	}
	d.curScreenX = sx
	d.curScreenY = sy
	a.draw()
	return nil
}

// advanceDragGhost promotes an armed drag past the threshold and materializes
// its ghost, once, for both buttons. A move hides the original by row id,
// because a by-lineage hide would vanish every clone; a creating drag shows
// both, since what lands is new.
func (a *App) advanceDragGhost(d *dragState, sx, sy float64) bool {
	if d.started {
		return true
	}
	dxs := sx - d.startScreenX
	dys := sy - d.startScreenY
	if dxs*dxs+dys*dys < dragThreshold*dragThreshold {
		return false
	}
	d.started = true
	if d.tileID == "" && !d.isTemplate {
		return true
	}
	size := d.srcCellSize
	if size <= 0 {
		// A template drag has no srcCellSize: the palette's lives in screen px,
		// not cells. A right-drag clone keeps the bare cellPx fallback.
		size = cellPx
		if src := a.tree.FindPane(d.originPaneID); src != nil && !d.intent.Creates() {
			if v, ok := src.Live(); ok {
				size = cellPx * v.Zoom()
			}
		}
	}
	a.ghost = &ghost{
		tile:              d.snapshotTile,
		Flight:            anim.Flight{X: d.originScreenX, Y: d.originScreenY},
		paneID:            d.originPaneID,
		displayedCellSize: size,
		targetCellSize:    size,
	}
	if d.tileID != "" && !d.intent.Creates() {
		a.ghost.hiddenTileID = d.tileID
		a.ghost.hiddenPaneID = d.originPaneID
	}
	return true
}

func (a *App) onMouseUp(this js.Value, args []js.Value) any {
	sx, sy := mouseXY(args[0], a.canvas)
	a.emit(traceevent.Release(a.paneIDAt(sx, sy), args[0].Get("button").Int()))
	switch gesture.Release(args[0].Get("button").Int(), a.armed()) {
	case gesture.FinishRightDrag:
		a.finishRightDrag(sx, sy)
	case gesture.FinishLeftResize:
		// The move applied the ratio live; the release decides the collapse.
		a.finishLeftResize()
	case gesture.FinishLeftDrag:
		a.finishLeftDrag(sx, sy)
	}
	return nil
}

func paneRectFor(a *App, p *pane.Pane) pane.Rect {
	rects := a.layoutPanes()
	if r, ok := rects[p.ID]; ok {
		return r
	}
	return pane.Rect{}
}

// tileDragInFlight is the state in which the source pane's + button turns into
// the trashcan delete target.
func (a *App) tileDragInFlight() bool {
	return a.dragging != nil && a.dragging.started && a.dragging.tileID != ""
}

// overDeleteButton takes the drag explicitly, because the commit path clears
// a.dragging before deciding what the drop means. Reading the field would say
// false at release, and the tile would land under the trashcan.
func (a *App) overDeleteButton(d *dragState, sx, sy float64) bool {
	if d == nil || !d.started || d.tileID == "" {
		return false
	}
	return a.pointInPlus(sx, sy)
}

// attemptDescentOrAscent routes a bare left-click, which only ever descends.
// It resolves the tile's facts and obeys gesture.DecideTileClick; inNewPane is
// the ctrl-click ask. Which frame a descent pushes is the tile's declaration;
// see nav.go.
func (a *App) attemptDescentOrAscent(p *pane.Pane, r pane.Rect, sx, sy float64, inNewPane bool) bool {
	if p.ContentID() != "" {
		// Ascent lives on the middle button and the bar's crumb click.
		return false
	}
	cellX, cellY, ok := cellAtScreen(p, r, sx, sy)
	if !ok {
		return false
	}
	hit := a.tileAtCell(p, cellX, cellY)
	if hit == nil {
		return false
	}
	writable, _ := a.gridWritable(hit.GridId)
	switch gesture.DecideTileClick(gesture.ClickInput{
		Well:           rpc.IsWellKind(hit.Kind),
		ContentDescent: rpc.IsContentDescentKind(hit.Kind),
		Workspace:      rpc.IsWorkspaceKind(hit.Kind),
		URL:            hit.Kind == rpc.KindURL,
		URLEmpty:       hit.UrlString == "",
		Page:           rpc.PageContent(hit),
		Writable:       writable,
		LeafLink:       rpc.LeafLink(hit),
		DeadLink:       a.deadLink(hit),
		SplitNav:       inNewPane,
	}) {
	case gesture.ClickConfigureURL:
		a.openConfigureURL(p, hit)
		return true
	case gesture.ClickNone:
		return false
	case gesture.ClickDescendSplit:
		target, _ := a.splitBelowForOpen(p)
		a.descend(target, hit)
		return true
	}
	a.descend(p, hit)
	return true
}

// totalTransitionMs is the same for a descent and an ascent, so they feel
// symmetric. It is a var only for the e2e-only setTransitionMs testhook.
var totalTransitionMs = 350.0

// zoomDistFactor scales log-zoom distance to perceived px so animation time can
// be split between pan and zoom. A zoom by factor e is about 256 px.
const zoomDistFactor = 4.0

// A descent through a link tile, a namespace crossing, lives in descend
// (nav.go), on the same path a plain well takes.

// persistedGridView reads the framing the grid at (anchor, path) was left at
// from the row that owns it. It restores every ascent with no session state,
// where 0,0 at zoom 1 would be a framing the user never set.
func (a *App) persistedGridView(p *pane.Pane, anchor string, path []string) (rpc.Framing, bool) {
	size, ok := paneRectFor(a, p).Size()
	if !ok {
		return rpc.Framing{}, false
	}
	if len(path) == 0 {
		return a.storedRootView(anchor, size)
	}
	g, found := a.c.Grid(a.gridIDForPathFrom(anchor, path[:len(path)-1]))
	if !found {
		return rpc.Framing{}, false
	}
	t, found := g.Tiles[path[len(path)-1]]
	if !found {
		return rpc.Framing{}, false
	}
	return zoomtrans.StoredView(wellOf(t), size, cellPx)
}

// storedRootView is the read side of persistFraming's root arm, the same 1x1
// synthetic doorway inverted, at pane size s. A root's framing rides its
// PluginInfo; false when it has none.
func (a *App) storedRootView(anchor string, s zoomtrans.Size) (rpc.Framing, bool) {
	pl, found := a.pluginByRoot(anchor)
	if !found {
		return rpc.Framing{}, false
	}
	w := zoomtrans.Well{W: 1, H: 1, View: rpc.ViewOf(pl.RootViewCx, pl.RootViewCy, pl.RootViewZoom)}
	if _, visited := w.View.Framing(); !visited {
		return rpc.Framing{}, false
	}
	return zoomtrans.StoredView(w, s, cellPx)
}

// splitBelowForOpen runs pane.SplitBelowForOpen, the one programmatic split,
// for the focused pane p. The shed ascent is unanimated: no ascent was asked.
func (a *App) splitBelowForOpen(p *pane.Pane) (target *pane.Pane, split bool) {
	prev := a.tree.Focus
	target, split, shed := a.tree.SplitBelowForOpen(paneRectFor(a, p))
	// The split moves focus to the new pane, so the menu has to be told, or
	// it stays open on a pane that no longer has focus.
	a.menu.TransferFocus(prev, a.tree.Focus)
	if shed {
		a.ascend(target, 1, false)
	}
	return target, split
}

func mouseXY(ev js.Value, canvas js.Value) (float64, float64) {
	rect := canvas.Call("getBoundingClientRect")
	x := ev.Get("clientX").Float() - rect.Get("left").Float()
	y := ev.Get("clientY").Float() - rect.Get("top").Float()
	return x, y
}
