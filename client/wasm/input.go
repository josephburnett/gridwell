//go:build js && wasm

package main

import (
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/dragdrop"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/gesture"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/panebox"
	"github.com/josephburnett/gridwell/client/wsbar"
	"github.com/josephburnett/gridwell/client/zoomtrans"
)

// This file is the canvas pointer event flow, in the order the events
// arrive: install the listeners, route a move or a release to whichever
// gesture is armed, resolve the pane and cell under the cursor, then the
// four handlers — wheel, mousedown, mousemove, mouseup — and the drag
// promotion they share. It gathers impure facts (what is under the cursor,
// what is live, what is armed) and hands them to the pure deciders
// (client/gesture, client/dragdrop, client/zoomtrans, client/pane); the
// verdicts it cannot execute inline leave through the files below.
//
// What the flow reaches for lives next to itself, not here: the left
// drag's commit and the ghost animation in drop_commit.go, the right
// button in right_button.go, the drop gather in drop_target.go, the + menu
// swatch drag in palette_drag.go, the create RPCs in create_tile.go, the
// scratch-grid visits in ephemeral.go, and the ascent text write-back in
// text_flush.go, which owns every path text bytes take to the server.

// installCanvasInput attaches the mouse listeners: presses and the wheel on
// the canvas, where a gesture can only start, and move and release at the
// window, so an in-flight gesture survives whatever the pointer crosses.
// Gridwell navigation is mouse-only: every gesture — move, descend, ascend,
// resize, clone, delete — is reachable through the left, right, and middle
// buttons, a drag, and the scroll wheel. There are no navigation
// keybindings.
//
// Keyboard events are only used in one place: when the focused pane is
// descended into a URL tile, every key (including browser-chrome
// shortcuts like Ctrl+W, Ctrl+T, F5, F11, F12) is forwarded to the
// remote Chromium tab and preventDefault'd locally so the user can
// drive the remote page exactly like a normal browser tab. When no
// pane is descended into a URL tile, keystrokes pass through to the
// browser unmolested (so F12 still opens devtools, Ctrl+L still
// focuses the address bar, etc.).
func (a *App) installCanvasInput() {
	a.canvas.Call("addEventListener", "wheel", js.FuncOf(a.onWheel))
	a.canvas.Call("addEventListener", "mousedown", js.FuncOf(a.onMouseDown))
	// move and up listen at the window, in the capture phase, never on the
	// canvas. A gesture armed by a canvas mousedown keeps tracking whatever
	// the pointer crosses: a text descent floats a DOM overlay — the
	// textarea or the rendered view — above the canvas, so a fast divider
	// drag whose next mousemove jumps into that rect hit-targets the
	// overlay, and a canvas-scoped listener would hear neither the move nor
	// the release, wedging the drag and leaving it armed after the button
	// came up. Capture also beats any stopPropagation an overlay library
	// such as xterm might do. When no gesture is in flight, events that did
	// not target the canvas are ignored, so overlay-local behavior — typing,
	// selection, terminal input — is untouched.
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
	// Suppress the browser's context menu on the canvas; right-click is
	// the clone/resize gesture stem, not a menu.
	a.canvas.Call("addEventListener", "contextmenu", js.FuncOf(func(this js.Value, args []js.Value) any {
		args[0].Call("preventDefault")
		return nil
	}))
	// Window-level (not canvas-level) so the content-zoom chord is caught
	// regardless of where in the document focus happens to sit.
	a.win.Call("addEventListener", "keydown", js.FuncOf(a.onKeyDown))
	// Touch screens: translate single-finger touch into the same mouse
	// gestures (see touch.go).
	a.installTouchInput()
}

// onKeyDown handles the one window-level chord the canvas owns: Ctrl/Cmd
// +/-/0 content zoom. Everything else belongs to whichever DOM overlay or
// native view has focus (textarea, xterm, WebContentsView) — the canvas
// never sees those keystrokes.
func (a *App) onKeyDown(_ js.Value, args []js.Value) any {
	if len(args) == 0 {
		return nil
	}
	// Ctrl/Cmd +/-/0 zooms a descended tile's content, checked first so
	// Electron's built-in page zoom never double-fires.
	a.handleContentZoomKey(args[0])
	return nil
}

// gestureInFlight reports whether any canvas pointer gesture is armed — the
// states onMouseMove/onMouseUp track across moves. This is the routing gate
// for the window-level move/up listeners: while true, every move/up belongs
// to the gesture regardless of what element the pointer is over.
func (a *App) gestureInFlight() bool {
	return a.leftResize != nil || a.rightDrag != nil || a.dragging != nil
}

// Bit masks for MouseEvent.buttons: which buttons are held right now, as
// opposed to MouseEvent.button, which names the one that changed.
const (
	buttonsLeft  = 1
	buttonsRight = 2
)

// recoverLostRelease resolves an armed gesture whose release we never saw: the
// button came up outside the window, or over a surface that swallowed the
// event. A move that reports the gesture's own button already up IS that
// release, arriving late, so each state finishes through its own commit path —
// never by being cleared, which would drop what the user did.
//
// It is the one owner of that recovery for all three armed states. Written per
// state, the left drag was simply forgotten: a lost release left a.dragging
// armed forever, and with it an immortal ghost, a source tile hidden under it,
// and every live view parked, since liveOverlaysHidden reads the same field.
//
// The order matches onMouseUp's. A right-drag from a tile's center arms
// a.dragging alongside a.rightDrag, and finishRightDrag is what commits that
// pair, so the right case must be tried before the plain-drag one.
//
// Reports whether it resolved something, in which case the move is spent.
func (a *App) recoverLostRelease(buttons int, sx, sy float64) bool {
	switch {
	case a.leftResize != nil && buttons&buttonsLeft == 0:
		// The same collapse decision an on-canvas release makes, from the last
		// applied cursor, never this stray re-entry point.
		a.finishLeftResize()
		return true
	case a.rightDrag != nil && buttons&buttonsRight == 0:
		a.finishRightDrag(sx, sy)
		return true
	case a.dragging != nil && buttons&buttonsLeft == 0:
		// finishLeftDrag refuses a creating drag (a right-button copy or link
		// armed in parallel), which stays armed for its own button's release.
		return a.finishLeftDrag(sx, sy)
	}
	return false
}

// paneAtScreen returns the pane (and its rect) under the given screen coords,
// or (nil, pane.Rect{}, false) if no pane covers the point.
func (a *App) paneAtScreen(sx, sy float64) (*pane.Pane, pane.Rect, bool) {
	rects := a.layoutPanes()
	for id, r := range rects {
		if r.Contains(sx, sy) {
			return a.tree.FindPane(id), r, true
		}
	}
	return nil, pane.Rect{}, false
}

// menuPaneForPointer resolves the pane an open palette's pointer events belong
// to. That is the menu's OWN pane, never the pane under the cursor: the
// popover is anchored to the bar slot at the bottom of the window and floats
// over whatever pane happens to sit there, and every swatch rect is laid out
// for the menu's pane. Routing by the pane at the pointer highlights nothing
// on a stacked layout, and at the press it would move focus out of the very
// menu being used.
//
// It is the one owner of that routing, shared by the press (onMouseDown) and
// the hover (onMouseMove); the release needs none, because an armed template
// drag owns its own release and carries its origin pane. Reports false when
// the menu is closed or its pane is gone — the nil pane paneAtScreen can hand
// back has no way in here.
func (a *App) menuPaneForPointer() (*pane.Pane, pane.Rect, bool) {
	mp := a.tree.FindPane(a.menu.PaneID()) // PaneID is "" while closed
	if mp == nil {
		return nil, pane.Rect{}, false
	}
	return mp, paneRectFor(a, mp), true
}

// cellAtScreen returns the integer cell that contains screen point (sx, sy)
// inside the given pane. It floors — which cell is the cursor in — rather
// than rounding, since round-half makes clicks in the lower-right half of a
// tile miss.
func cellAtScreen(p *pane.Pane, r pane.Rect, sx, sy float64) (int64, int64) {
	return paneToDragdrop(p, r).CellAt(sx, sy)
}

// tileAtCell returns the tile in the pane's grid that covers the given cell,
// or nil. Used for click hit-testing.
func (a *App) tileAtCell(p *pane.Pane, cellX, cellY int64) *rpc.Tile {
	gid := a.gridIDForPane(p)
	g, ok := a.c.Grid(gid)
	if !ok {
		return nil
	}
	for _, n := range g.Tiles {
		if dragdrop.TileContainsCell(n.X, n.Y, n.W, n.H, cellX, cellY) {
			nn := n
			return &nn
		}
	}
	return nil
}

// focusToPane transfers wasm focus to pane p. If focus actually changes it
// calls menu.TransferFocus (which closes the menu when it was on the old pane),
// refreshes the file overlay, and draws. Returns true when focus changed.
//
// This is the single focus-transfer owner; every press path calls it: canvas
// onMouseDown, onForwardedRightDown, onForwardedLeftDown, and the opening of a
// live view's native context menu (onForwardedContextMenu). Calling it is
// always safe, and a no-op on the same pane.
func (a *App) focusToPane(p *pane.Pane) bool {
	prev := a.tree.Focus
	_ = a.tree.SetFocus(p.ID)
	if !a.menu.TransferFocus(prev, a.tree.Focus) {
		return false
	}
	// Focus moved, so the text-mode chrome follows. The textarea overlay
	// only ever lives over the focused pane, so without this call a click on
	// a sibling pane in text mode leaves the textarea stranded.
	a.refreshFileOverlay()
	a.draw()
	return true
}

func (a *App) onWheel(this js.Value, args []js.Value) any {
	args[0].Call("preventDefault")
	dy := args[0].Get("deltaY").Float()
	sx, sy := mouseXY(args[0], a.canvas)
	// A wheel over the bar zooms the pane it rides — the focused one — as if
	// the cursor were at that pane's center: the escape hatch for a grid tiled
	// wall to wall with wells, where every content position claims the well
	// zoom and no empty spot remains. The band is below every pane, so this is
	// resolved before the pane hit-test; beside the bar the row is plain
	// background with no pane under it, so the hit-test below finds nothing
	// there either.
	if bx, top, bw, barOK := a.bottomBarRect(); barOK &&
		wsbar.Where(sx, sy, bx, top, bw) == wsbar.ZoneBar {
		if fp := a.tree.FocusedPane(); fp != nil && fp.ContentID() == "" {
			fr := a.paneRectByID(fp.ID)
			a.wheelZoomPaneAt(fp, fr, dy, fr.X+fr.W/2, fr.Y+fr.H/2)
		}
		return nil
	}
	p, r, ok := a.paneAtScreen(sx, sy)
	if !ok {
		return nil
	}
	// Routing is the pure gesture.ClassifyWheel: this handler only resolves
	// the impure facts (live view attached? cursor in the content box? an
	// enterable well under the cursor, and how much of the view it covers?)
	// and executes the verdict.
	var hoverWell *rpc.Tile
	wellCoverage := 0.0
	if p.ContentID() == "" {
		if t := a.tileAtScreen(p, r, sx, sy); t != nil && rpc.IsWellKind(t.Kind) && t.ChildGridID != "" {
			hoverWell = t
			ps := paneToDragdrop(p, r)
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
		// A live URL view owns the content box and scrolls itself; a stray
		// wheel reaching the canvas must not zoom the pane underneath.
		args[0].Call("preventDefault")
		return nil
	case gesture.WheelScrollDoc:
		// A text tile has a fixed scale and no zoom, so the wheel scrolls
		// the rendered window vertically. In text mode the textarea scrolls
		// itself and the event never reaches the canvas.
		p.TextScrollY += dy
		if p.TextScrollY < 0 {
			p.TextScrollY = 0
		}
		a.draw()
		a.scheduleURLUpdate()
		return nil
	case gesture.WheelIgnore:
		return nil
	case gesture.WheelZoomWell:
		// The wheel zooms the grid inside the hovered well — its stored
		// preview framing, the one fact the renderer reads per frame — not
		// the grid the pane shows. The math is zoomtrans.WellWheelView, the
		// same cursor-anchored kernel as the pane zoom, in preview space.
		// The cache is patched per notch, and the settle persister posts one
		// framing write per tile at flush.
		ps := paneToDragdrop(p, r)
		x0, y0 := ps.CellToScreen(float64(hoverWell.X), float64(hoverWell.Y))
		x1, _ := ps.CellToScreen(float64(hoverWell.X)+1, float64(hoverWell.Y))
		parentCell := x1 - x0
		wpx := float64(hoverWell.W) * parentCell
		hpx := float64(hoverWell.H) * parentCell
		zw := wellOf(hoverWell)
		// The float center accumulates across the burst: the first notch
		// seeds from the framing the well is showing — a never-visited one
		// shows its footprint's center, not the corner, the same
		// EffectiveCenter the preview under the cursor was drawn with — and
		// later notches feed the drift back in, so a cursor-anchored zoom
		// travels.
		cx0, cy0 := zoomtrans.EffectiveCenter(zw)
		if st, ok := a.persist.wellWheelPending[hoverWell.ID]; ok {
			cx0, cy0 = st.cx, st.cy
		}
		cx1, cy1, ratio, changed := zoomtrans.WellWheelView(dy, zw, parentCell,
			sx-(x0+wpx/2), sy-(y0+hpx/2), cx0, cy0, zoomFactor, wellZoomRatioMin, wellZoomRatioMax)
		if !changed {
			return nil
		}
		updated := *hoverWell
		updated.ViewCx = cx1
		updated.ViewCy = cy1
		updated.ViewZoom = ratio
		a.c.Apply(rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: updated}})
		a.persist.wellWheelPending[hoverWell.ID] = wellWheelDrift{
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

// wheelZoomPaneAt zooms pane p anchored at screen point (sx, sy): the world
// point under the anchor stays under it after the zoom, map-style. The
// bar-band wheel calls this with the pane's center.
func (a *App) wheelZoomPaneAt(p *pane.Pane, r pane.Rect, dy, sx, sy float64) {
	ps := paneToDragdrop(p, r)
	cellX, cellY := ps.ScreenToCell(sx, sy)
	// Step clamp + cursor-anchored re-center is the pure zoomtrans.WheelZoom.
	p.Zoom, p.Cx, p.Cy = zoomtrans.WheelZoom(dy, p.Zoom, p.Cx, p.Cy, cellX, cellY, zoomFactor, zoomMin, zoomMax)
	a.draw()
	a.scheduleURLUpdate()
}

// grabTile records that the drag picked up tile n: the id and the snapshot
// the ghost and the commit read, the cursor's offset inside the tile (in that
// tile's own cell units, so the grab point tracks the cursor at any zoom),
// and the tile's top-left in screen coords for the snap-back. The caller
// resolves the cursor and the corner in whichever space the tile lives —
// the parent grid, a well's child preview, or the clone arm's parent grid —
// and every arm records the same six fields the same way.
func (d *dragState) grabTile(n *rpc.Tile, cursorCellX, cursorCellY, tlX, tlY float64) {
	d.tileID = n.ID
	d.snapshotTile = *n
	d.cellOffsetX = cursorCellX - float64(n.X)
	d.cellOffsetY = cursorCellY - float64(n.Y)
	d.originScreenX = tlX
	d.originScreenY = tlY
}

func (a *App) onMouseDown(this js.Value, args []js.Value) any {
	sx, sy := mouseXY(args[0], a.canvas)
	// Block all input gestures while a viewport transition is animating
	// — keeps the tree and viewport state atomic across the zoom.
	if a.trans.Any() {
		return nil
	}
	// Notice strip: it occupies the band layoutPanes reserved below every
	// pane, so a click there can't be meant for a pane. Clicking a row
	// dismisses that notice (errsurface owns the hit geometry).
	if stripH := errsurface.StripHeight(a.errs.Len()); stripH > 0 && sy >= a.height-stripH {
		if a.errs.DismissAt(sy, a.height-stripH) {
			a.draw()
		}
		return nil
	}
	// The bottom bar. A left-click on a pane-tile crumb goes there, closing
	// everything deeper — the one gesture that crosses the level boundary —
	// and a right-click renames. A left-click on a chain crumb ascends.
	if a.bottomBarClick(sx, sy, args[0].Get("button").Int()) {
		args[0].Call("preventDefault")
		return nil
	}
	// A left-click inside the open palette belongs to the palette, routed
	// before pane resolution: the popover is anchored to the bar slot and
	// floats over whatever pane happens to be under it, so resolving that
	// pane first would transfer focus and close the very menu being used.
	// Landing on a swatch starts a template drag; missing one swallows the
	// click so the popover stays open.
	if mp, mr, ok := a.menuPaneForPointer(); ok && args[0].Get("button").Int() == 0 {
		if a.pointInPalette(mp, sx, sy) {
			// The plugin section's disclosure strip: press it and the section
			// folds or unfolds in place. It is not a swatch, so no drag arms
			// and the release does nothing, and it changes only the menu —
			// the pane keeps its focus and its selection.
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
	// focusToPane transfers focus, closes the menu on the de-focused pane
	// through menu.TransferFocus, refreshes the text overlay, and draws — all
	// in one call, so no path can forget SyncFocus.
	prevFocus := a.tree.Focus
	a.focusToPane(p)
	button := args[0].Get("button").Int()
	if button == 2 {
		args[0].Call("preventDefault")
		// The modifier is read here, at the press, and never again: ctrl
		// flips a right-drag from copy to link (rightDragIntent).
		a.onRightDown(p, r, sx, sy, rightDragIntent(args[0]))
		return nil
	}
	if button == 1 {
		// The middle button ascends the pane under the cursor: the in-pane
		// shortcut for the bar's crumb-click ascent. preventDefault
		// suppresses the browser's middle-click autoscroll.
		args[0].Call("preventDefault")
		a.menu.Close()
		a.ascendPane(p)
		return nil
	}
	if button != 0 {
		return nil
	}

	// (A left-click inside the open palette was already claimed before pane
	// resolution — see the top of this handler.)

	// A left-drag on a pane boundary resizes the divider. It is checked
	// first, so a grab near the edge wins over content interactions, and
	// falls through when there is no divider on that side. preventDefault,
	// as on the right-button path: the unprevented default action — native
	// selection or drag — engages past the OS drag threshold on a fast drag
	// and steals the pointer from the canvas mid-resize. That steal is
	// invisible to synthetic input, so the e2e pins the prevented flag
	// instead.
	if a.armLeftResize(r, sx, sy) {
		args[0].Call("preventDefault")
		return nil
	}

	// In a content descent — text, url, shell — every interactive surface is
	// a DOM overlay or a native view that owns its own clicks (the textarea,
	// the rendered div, xterm, the WebContentsView), and the bar slot owns
	// the buttons. A canvas left-click reaching here is pane chrome or
	// margin, and ascent lives on the middle button and the bar crumbs, so
	// it is swallowed whole.
	if p.ContentID() != "" {
		return nil
	}

	// The + button lives in the bar slot; barSlotClick toggles the menu
	// before a click ever reaches a pane. A click inside the popover was
	// claimed by menuPaneForPointer at the top of this handler, whichever pane
	// the popover floats over, so what is left here is a click outside it:
	// dismiss the menu and fall through to normal interaction.
	if a.menu.OpenOn(p.ID) {
		a.menu.Close()
		a.draw()
		// fall through so the click also pans / selects
	}

	cellX, cellY := cellAtScreen(p, r, sx, sy)
	n := a.tileAtCell(p, cellX, cellY)
	parentCell := cellPx * p.Zoom
	ps := paneToDragdrop(p, r)
	a.dragging = &dragState{
		originPaneID:  p.ID,
		originFocused: prevFocus == p.ID,
		splitNav:      args[0].Get("ctrlKey").Truthy(),
		tileID:        "",
		startScreenX:  sx,
		startScreenY:  sy,
		curScreenX:    sx,
		curScreenY:    sy,
		// Default source = the focused pane's leaf grid; overridden
		// below if we land on a child preview tile.
		srcGridID:   a.gridIDForPane(p),
		srcCellSize: parentCell,
	}
	if n != nil {
		// "Pull out of well" gesture: cursor is on an open well; if a
		// child preview tile sits at the cursor, treat *that* as the
		// drag source instead of the well.
		if child := a.childTileAtScreen(p, r, n, sx, sy); child != nil {
			cp := wellPreviewFor(ps, n)
			cxF, cyF := cp.ChildCellAtScreen(sx, sy)
			tlX, tlY := cp.CellToScreen(float64(child.X), float64(child.Y))
			a.dragging.grabTile(child, cxF, cyF, tlX, tlY)
			a.dragging.srcGridID = n.ChildGridID
			a.dragging.srcCellSize = cp.CellPx
			return nil
		}
		// Regular parent-grid drag of the well (or a non-well tile).
		cx, cy := ps.ScreenToCell(sx, sy)
		tlX, tlY := ps.CellToScreen(float64(n.X), float64(n.Y))
		a.dragging.grabTile(n, cx, cy, tlX, tlY)
	}
	return nil
}

func (a *App) onMouseMove(this js.Value, args []js.Value) any {
	sx, sy := mouseXY(args[0], a.canvas)
	// A gesture whose release we never saw ends here, before anything else
	// reads it. One owner for all three armed states.
	if a.recoverLostRelease(args[0].Get("buttons").Int(), sx, sy) {
		return nil
	}
	// Left-button pane resize takes precedence over everything else.
	if a.leftResize != nil {
		a.onLeftResizeMove(sx, sy)
		return nil
	}
	// URL-stream forwarding: if the cursor is over a live URL view's own
	// content box, the move belongs to the page. Any in-flight drag (left or
	// right button) keeps gridwell in charge of the move so the user can drag
	// clones / resize past a URL pane without the page seeing it — which is
	// already true here, since a drag parks every live view and
	// liveViewOwnsPoint asks about the park.
	if a.rightDrag == nil && a.dragging == nil {
		if p, r, ok := a.paneAtScreen(sx, sy); ok && a.liveViewOwnsPoint(p, r, sx, sy) {
			// The live view owns its own cursor (the canvas won't get moves
			// over it anyway). Default cursor.
			a.canvas.Get("style").Set("cursor", "")
		} else {
			// The canvas owns this point — no live view, or one that is
			// parked, or the pane's border band: show a resize cursor when
			// over a grabbable split divider (the grab band is far wider
			// than the 1px line), otherwise clear.
			a.canvas.Get("style").Set("cursor", a.dividerResizeCursor(sx, sy))
		}
	}
	// Right-button gestures take precedence so a drag that started on
	// the right button doesn't accidentally invoke left-button code
	// paths (e.g., menu hover) below.
	if a.rightDrag != nil {
		a.onRightMove(sx, sy)
		return nil
	}
	// Track palette hover regardless of drag state, through the same router
	// the press uses: the popover's swatches are laid out for the menu's OWN
	// pane, so hit-testing them against the pane under the cursor highlights
	// nothing whenever the popover floats over a different pane.
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
	// Promote to "started" once the cursor has moved past the threshold,
	// materializing the ghost — the one threshold, shared with the
	// right-button clone drag (advanceCloneDrag).
	if !a.advanceDragGhost(d, sx, sy) {
		return nil
	}
	if d.tileID == "" && !d.isTemplate {
		// Pan the source pane's parent-grid view smoothly. A pan drag only
		// arms in grid mode, since a content descent swallows the mousedown,
		// so there is no text-scroll arm here.
		focused := a.tree.FindPane(d.originPaneID)
		if focused != nil {
			cellSize := cellPx * focused.Zoom
			focused.Cx -= (sx - d.curScreenX) / cellSize
			focused.Cy -= (sy - d.curScreenY) / cellSize
		}
	} else if a.ghost != nil {
		// Update the ghost from the same dragdrop.DecideDrop verdict the
		// left-drag commit (onMouseUp) uses, so a previewed action cannot
		// differ from the committed one. The flavor rides d.intent.
		a.previewDrop(d, sx, sy)
	}
	d.curScreenX = sx
	d.curScreenY = sy
	a.draw()
	return nil
}

// advanceDragGhost promotes an armed drag past the drag threshold and
// materializes its ghost, once, for both buttons: the left-drag move/template
// path (onMouseMove) and the right-drag clone path (advanceCloneDrag). It
// reports whether the drag is started — false means the cursor has not left
// the press point yet, and the caller decides what a not-yet-started move
// does (the left path drops the move entirely; the right path still tracks
// the cursor).
//
// The two flavors differ in exactly two places, both kept explicit:
//   - the hide. A move hides the original at its stored position so we don't
//     see two copies of the same stone; a creating drag (d.intent.Creates —
//     a right-drag copy or link) shows both, since what lands is a new stone.
//     Hiding is by row id: a clone is a different row that looks the same, so
//     a by-lineage hide would make every clone vanish whenever its sibling is
//     picked up (dragdrop.HiddenMatch and its test cover the predicate). It
//     lives on the ghost, because the hide outlives the drag through the
//     snap-back and dies with the ghost.
//   - the missing-cell-size fallback (below).
//
// A pan drag — no tile and no template — materializes no ghost; a right-drag
// always carries a tile (armRightClone), so the guard only ever excludes the
// left path's pan.
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
		// Template drag: srcCellSize wasn't set by the palette (it lives in
		// screen px, not cells), so use the focused pane's parent cell size.
		// A right-drag clone is armed from a real tile with a real cell size
		// and keeps the bare cellPx fallback it has always had.
		size = cellPx
		if src := a.tree.FindPane(d.originPaneID); src != nil && !d.intent.Creates() {
			size = cellPx * src.Zoom
		}
	}
	a.ghost = &ghost{
		tile:              d.snapshotTile,
		paneID:            d.originPaneID,
		screenX:           d.originScreenX,
		screenY:           d.originScreenY,
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
	// Right-button release commits a pending pane-management gesture.
	if a.rightDrag != nil && args[0].Get("button").Int() == 2 {
		sx, sy := mouseXY(args[0], a.canvas)
		a.finishRightDrag(sx, sy)
		return nil
	}
	// A left-button release ends an in-flight pane-boundary resize: the
	// ratio was applied live during the move, and the release decides the
	// collapse — crush past the wall and let go to close that side.
	if a.leftResize != nil && args[0].Get("button").Int() == 0 {
		a.finishLeftResize()
		return nil
	}
	// URL descent: a live view's content box is handled by the native view,
	// so swallow the matching mouseup over it rather than leaking it into a
	// gridwell gesture. Whether the view owns that point is
	// panebox.LiveViewOwnsPoint's decision, not this handler's: an armed
	// gesture parks every live view, so a release that ends one is never
	// swallowed here and always reaches the commit below.
	sx, sy := mouseXY(args[0], a.canvas)
	if p, r, ok := a.paneAtScreen(sx, sy); ok && args[0].Get("button").Int() == 0 &&
		a.liveViewOwnsPoint(p, r, sx, sy) {
		return nil
	}
	a.finishLeftDrag(sx, sy)
	return nil
}

func paneRectFor(a *App, p *pane.Pane) pane.Rect {
	rects := a.layoutPanes()
	if r, ok := rects[p.ID]; ok {
		return r
	}
	return pane.Rect{}
}

// tileDragInFlight reports whether a real tile (not a pan or a palette
// template) is currently being dragged past the start threshold — the
// state in which the source pane's + button turns into the trashcan
// delete target.
func (a *App) tileDragInFlight() bool {
	return a.dragging != nil && a.dragging.started && a.dragging.tileID != ""
}

// overDeleteButton reports whether (sx, sy) is over the delete target for the
// in-flight drag d: the bar slot's trashcan, one global target.
//
// It takes the drag explicitly rather than reading a.dragging, because the
// commit path clears a.dragging before deciding what the drop means: both
// onMouseUp and commitTileCenter do `d := a.dragging; a.dragging = nil`.
// Reading the field here would return false at release, and the tile would
// fall through to a normal move and be placed under the trashcan, even though
// the live-drag preview looked correct.
func (a *App) overDeleteButton(d *dragState, sx, sy float64) bool {
	if d == nil || !d.started || d.tileID == "" {
		return false
	}
	return a.pointInPlus(sx, sy)
}

// attemptDescentOrAscent routes a bare left-click, with no drag, at (sx, sy)
// inside pane p to the right navigation gesture. A left-click only ever
// descends; ascent is the middle button or the bar's crumb click (see
// ascendPane).
//
//   - On a well: descend into the well.
//   - On a markdown file: descend into the file.
//   - Otherwise: no-op (selection is handled by the bare-click path
//     in onMouseUp before this is invoked).
//
// inNewPane is the ctrl-click ask (DropNavigateSplit): the descent lands in
// a new pane split below, leaving this pane where it is. Only a descent
// splits — the url-configure prompt stays in place, and a click that would
// do nothing still does nothing.
//
// Returns true if a navigation gesture was performed (caller should skip
// further interpretation of the click).
func (a *App) attemptDescentOrAscent(p *pane.Pane, r pane.Rect, sx, sy float64, inNewPane bool) bool {
	if p.ContentID() != "" {
		// Inside a text/url/shell descent, a bare click isn't navigation:
		// ascent lives on the middle button and the bar's crumb click.
		return false
	}
	cellX, cellY := cellAtScreen(p, r, sx, sy)
	hit := a.tileAtCell(p, cellX, cellY)
	if hit == nil {
		return false
	}
	// An address-less url tile, dropped bare: the first descent is where the
	// address is asked for. A url link resolves its address through the
	// target and never prompts.
	if hit.Kind == rpc.KindURL && hit.URLString == "" && !hit.LeafLink() {
		a.openConfigureURL(p, hit)
		return true
	}
	if !rpc.IsWellKind(hit.Kind) && !rpc.IsContentDescentKind(hit.Kind) &&
		!rpc.IsWorkspaceKind(hit.Kind) {
		return false
	}
	// One descent: which kind of frame it pushes is the tile's declaration
	// (nav.go), not this call site's.
	target := p
	if inNewPane && !a.deadLink(hit) {
		// Ctrl asked for the descent in a new pane. A dead link descends
		// nowhere (descend's own guard), so it births no pane either.
		target = a.splitBelowForOpen(p)
	}
	a.descend(target, hit)
	return true
}

// totalTransitionMs is the total wall-clock duration of a descent or ascent
// transition. The same value serves both, so the two feel symmetric. It is a
// var, not a const, solely for the e2e-only setTransitionMs testhook, which
// stretches it so a spec can inject an event during a transition
// deterministically; production has no writer.
var totalTransitionMs = 350.0

// zoomDistFactor scales log-zoom distance to a "perceived px" unit so we
// can apportion animation time between pan and zoom phases. Tuned so a
// zoom by factor e ≈ 256 px-equivalent — about four cells. Bigger than
// pan distances tend to be in practice, so zoom phases get the bulk of
// the time, which matches the user's intent that the zoom is "the
// action" and the pan is "the setup".
const zoomDistFactor = 4.0

// Descents through a link tile — a namespace crossing — live in descend
// (nav.go): a mounted well and a cross-plugin clone take the same path a
// plain well does, with a frame pushed so ascent returns exactly here.

// persistedGridView resolves the framing the grid at (anchor, path) was left
// at, from the row that owns it: the containing well's framing for a nested
// grid, the plugin's persisted root view for a root. It is the restore for
// every ascent with no session state, such as a reload mid-descent, and the
// boot viewport; landing on 0,0 and zoom 1 instead would be a framing the
// user never set. ok=false when nothing is persisted or the owning row is not
// cached, and callers keep their own fallback then.
func (a *App) persistedGridView(p *pane.Pane, anchor string, path []string) (cx, cy, zoom float64, ok bool) {
	r := paneRectFor(a, p)
	if r.W <= 0 || r.H <= 0 {
		return 0, 0, 0, false
	}
	if len(path) == 0 {
		// A root grid: the read side of persistFraming's root arm, the same
		// 1×1 synthetic doorway inverted. A root's framing rides its
		// PluginInfo.
		var vcx, vcy, vzoom float64
		if pl, found := a.pluginByRoot(anchor); found {
			vcx, vcy, vzoom = pl.RootViewCx, pl.RootViewCy, pl.RootViewZoom
		}
		if vzoom <= 0 {
			return 0, 0, 0, false
		}
		w := zoomtrans.Well{W: 1, H: 1,
			ViewCx: vcx, ViewCy: vcy, ViewZoom: vzoom}
		cx, cy, zoom = zoomtrans.StoredView(w, r.W, r.H, cellPx)
		return cx, cy, zoom, true
	}
	g, found := a.c.Grid(a.gridIDForPathFrom(anchor, path[:len(path)-1]))
	if !found {
		return 0, 0, 0, false
	}
	t, found := g.Tiles[path[len(path)-1]]
	if !found {
		return 0, 0, 0, false
	}
	w := wellOf(&t)
	cx, cy, zoom = zoomtrans.StoredView(w, r.W, r.H, cellPx)
	return cx, cy, zoom, true
}

// splitBelowForOpen splits the focused pane p into a new lower half and
// returns the new pane, focused, ready to receive a descent or a visit. It
// is the one programmatic split: a link opened out of a live tile and a
// ctrl-click descent land in the same place. The universal pane minimum
// applies to programmatic splits too — a pane too short for two minimum
// panes returns p itself, so the caller's action lands in place instead of
// being silently dropped (SplitOnSideAt only clamps the ratio; rejecting a
// sub-minimum split is this caller's job). The clone inherits the source's
// content frame, which a live view cannot duplicate — the same rule as
// commitSplit — so the new pane ascends just the content level and shows
// the grid containing the tile. The ephemeral delete-on-ascent asks whether
// another pane still shows the tile, so that ascent never deletes the tile the
// source pane still shows. That shedding is instant: the clone is born in the
// same place as the source with nothing on screen to zoom out of, and the user
// made no ascent gesture, so animating it would be a second transition for one
// gesture — and one that lands wearing the trace of a departure that never
// happened. commitSplit's animated ascent is the other case: there the user
// asked, and the footprint is real.
func (a *App) splitBelowForOpen(p *pane.Pane) *pane.Pane {
	if !pane.CanSplit(pane.SideBottom, paneRectFor(a, p)) {
		return p
	}
	prev := a.tree.Focus
	newP, err := a.tree.SplitOnSideAt(pane.SideBottom, 0.5)
	if err != nil {
		return p
	}
	// SplitOnSideAt moves focus to the new pane itself, so the menu has to be
	// told, like every other path that moves focus. Without this the + menu
	// would stay open on a pane that no longer has focus.
	a.menu.TransferFocus(prev, a.tree.Focus)
	if newP.ContentID() != "" {
		a.ascend(newP, 1, false)
	}
	return newP
}

// mouseXY returns the click coordinates relative to the canvas.
func mouseXY(ev js.Value, canvas js.Value) (float64, float64) {
	rect := canvas.Call("getBoundingClientRect")
	x := ev.Get("clientX").Float() - rect.Get("left").Float()
	y := ev.Get("clientY").Float() - rect.Get("top").Float()
	return x, y
}

// (Right-button gesture handling lives in right_button.go.)
