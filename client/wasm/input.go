//go:build js && wasm

package main

import (
	"context"
	"github.com/josephburnett/gridwell/client/textedit"
	"math"
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/anim"
	"github.com/josephburnett/gridwell/client/caps"
	"github.com/josephburnett/gridwell/client/dragdrop"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/gesture"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/panebox"
	"github.com/josephburnett/gridwell/client/pluginhealth"
	"github.com/josephburnett/gridwell/client/shellconn"
	"github.com/josephburnett/gridwell/client/wsbar"
	"github.com/josephburnett/gridwell/client/zoomtrans"
)

// Animation durations in milliseconds. Tuned for "stone settling" feel.
const (
	snapMs     = 110.0
	snapBackMs = 220.0
)

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
// onMouseDown, onForwardedRightDown, onForwardedLeftDown. Calling it is
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
		if st, ok := a.wellWheelPending[hoverWell.ID]; ok {
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
		a.wellWheelPending[hoverWell.ID] = wellWheelDrift{
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
	if a.transition != nil {
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
	if a.menu.IsOpen() && args[0].Get("button").Int() == 0 {
		if mp := a.tree.FindPane(a.menu.PaneID()); mp != nil {
			mr := paneRectFor(a, mp)
			if a.pointInPalette(mp, sx, sy) {
				if idx := a.paletteTileIndexAt(mp, sx, sy); idx >= 0 {
					a.startPaletteDrag(mp, mr, idx, sx, sy)
				}
				return nil
			}
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
		a.onRightDown(p, r, sx, sy)
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
	if a.armLeftResize(p, r, sx, sy) {
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
	// before a click ever reaches a pane.
	// Mousedown inside the palette: starting a template drag if it
	// landed on a tile, or swallowing the click (keeps the popover
	// open) if it landed in the gutter. Click outside the popover
	// dismisses it and falls through to normal interaction.
	if a.menu.OpenOn(p.ID) {
		if a.pointInPalette(p, sx, sy) {
			idx := a.paletteTileIndexAt(p, sx, sy)
			if idx >= 0 {
				a.startPaletteDrag(p, r, idx, sx, sy)
				return nil
			}
			return nil
		}
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
	// Left-button pane resize takes precedence over everything else.
	if a.leftResize != nil {
		if args[0].Get("buttons").Int()&1 == 0 {
			// Left button released somewhere we didn't see — finish, with
			// the same collapse decision an on-canvas release makes (from
			// the last applied cursor, never this stray re-entry point).
			a.finishLeftResize()
			return nil
		}
		a.onLeftResizeMove(sx, sy)
		return nil
	}
	// URL-stream forwarding: if the cursor is over a pane that's
	// descended into a live URL tile, the move belongs to the page.
	// Any in-flight drag (left or right button) keeps gridwell in
	// charge of the move so the user can drag clones / resize past a
	// URL pane without the page seeing it.
	if a.rightDrag == nil && a.dragging == nil {
		if p, _, ok := a.paneAtScreen(sx, sy); ok && a.isURLDescent(p) {
			// A live pane's native view owns its own cursor (the canvas
			// won't get moves over it anyway); a frozen pane's letterboxed
			// preview has no pan gesture. Default cursor either way.
			a.canvas.Get("style").Set("cursor", "")
		} else {
			// Not hovering a URL descent pane: show a resize cursor when
			// over a grabbable split divider (the grab band is far wider
			// than the 1px line), otherwise clear.
			a.canvas.Get("style").Set("cursor", a.dividerResizeCursor(sx, sy))
		}
	}
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
	// Track palette hover regardless of drag state.
	if a.menu.IsOpen() {
		p, _, ok := a.paneAtScreen(sx, sy)
		hover := -1
		if ok && a.menu.OpenOn(p.ID) {
			hover = a.paletteTileIndexAt(p, sx, sy)
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
	// URL descent: the live content box is handled by the native view;
	// swallow the matching mouseup over it so it doesn't leak into a
	// gridwell gesture.
	sx, sy := mouseXY(args[0], a.canvas)
	if p, r, ok := a.paneAtScreen(sx, sy); ok && a.isURLDescent(p) {
		if a.urlViewFor(p.ID) != nil && pointInPaneContent(r, sx, sy) && args[0].Get("button").Int() == 0 {
			return nil
		}
	}
	if a.dragging == nil {
		return nil
	}
	// A right-button drag — a copy or a link — commits only through the
	// right-button release path (finishRightDrag → commitRightClone), which
	// clears a.dragging before reaching here. Reaching the move-commit with
	// one still armed means a non-right button came up mid-drag (e.g. the
	// user pressed and released the left button while right-dragging).
	// Ignore it so the gesture is never silently committed as a move — it
	// stays armed and the eventual right-button release still creates.
	if a.dragging.intent.Creates() {
		return nil
	}
	d := a.dragging
	a.dragging = nil
	// Reset any drag-time cursor change (e.g. "not-allowed" from
	// hovering a doc with the left button).
	a.canvas.Get("style").Set("cursor", "")
	sx, sy = mouseXY(args[0], a.canvas)

	// A plugin swatch clicked without dragging past the threshold enters
	// that plugin: the + menu's click-to-descend gesture. A drag instead
	// drops an exit-well link (commitTemplateDrop). The descent is the same
	// one a link tile takes — one verb, one pushed frame — through a
	// synthetic link tile placed at the pane's view center, so ascent lands
	// back exactly here.
	if d.isTemplate && d.item.isPlugin && !d.started {
		if fp := a.tree.FindPane(d.originPaneID); fp != nil {
			well := paletteItemGhostNode(d.item)
			well.X, well.Y = int64(math.Floor(fp.Cx-0.5)), int64(math.Floor(fp.Cy-0.5))
			a.descend(fp, &well, nil)
		}
		return nil
	}

	// A url swatch clicked without dragging past the threshold is an
	// ephemeral visit: open the url modal and, on submit, descend into a
	// live url tile created in the off-grid scratch grid — a page visited
	// without placing a tile. A drag instead places a real url tile
	// (commitTemplateDrop).
	if d.isTemplate && d.item.promotePane != "" && !d.started {
		return nil // a click on the current crumb: this is where you are
	}
	if d.isTemplate && d.item.primitive == tplURL && !d.started {
		// An ephemeral visit is a live view. On a host without one, a plain
		// browser, the modal would only produce a blank frozen tile, so say
		// why up front. Drag-create still places a real url tile.
		if !a.caps.LiveURL {
			a.menu.Close()
			a.reportErr(caps.GoLiveNotice())
			return nil
		}
		if fp := a.tree.FindPane(d.originPaneID); fp != nil {
			paneID := fp.ID
			a.menu.Close()
			// Check before the modal opens: typing a url into a visit that
			// cannot land would fail only on submit.
			if a.scratchOrReport(fp) == "" {
				return nil
			}
			a.openURLModal(a.urlSuggestCandidates(uuidOf(a.gridIDForPane(fp))),
				func(url string) {
					if vp := a.tree.FindPane(paneID); vp != nil {
						a.visitEphemeralURL(vp, url)
					}
				}, nil)
		}
		return nil
	}

	// A shell swatch clicked without dragging is an ephemeral shell: created
	// in the off-grid scratch grid, descended into, with the PTY spawned.
	// Ascent deletes it — the tile row and the tmux session with all its
	// processes, which the gray border warns about. A drag instead places a
	// real, persistent shell tile.
	if d.isTemplate && d.item.primitive == tplShell && !d.started {
		if fp := a.tree.FindPane(d.originPaneID); fp != nil {
			a.menu.Close()
			a.visitEphemeralShell(fp) // reports if there is nowhere to open
		}
		return nil
	}

	// Snapshot every world-read the drop decision needs, once, using the
	// local d, since a.dragging is already nil above. DecideDrop then picks
	// the action and the switch executes the side effects. onMouseMove
	// gathers the same DropInput for the ghost preview, so preview and commit
	// cannot diverge. The flavor is d.intent, which is IntentMove here: a
	// right-button drag commits via the right path above.
	in, t, dropX, dropY := a.dropInputAt(d, sx, sy, true /* placement */)

	verdict := dragdrop.DecideDrop(in)
	switch verdict {
	case dragdrop.DropFocusOnly:
		// The click's only job was moving focus (done at mousedown).
		a.draw()
		return nil

	case dragdrop.DropNavigate, dragdrop.DropNavigateSplit:
		// Bare click (no movement) on an already-focused pane: navigation.
		// The split flavor is the same click with ctrl held at press: the
		// descent lands in a new pane below; everything else is identical.
		focused := a.tree.FindPane(d.originPaneID)
		if focused == nil {
			a.draw()
			return nil
		}
		r := paneRectFor(a, focused)
		// Try descent/ascent first — a click on a well, a content
		// tile, or in the edge band kicks off navigation. Selection
		// only applies to other cases (e.g., clicking a tile to
		// outline it without descending).
		if a.attemptDescentOrAscent(focused, r, sx, sy,
			verdict == dragdrop.DropNavigateSplit) {
			a.scheduleURLUpdate()
			return nil
		}
		cellX, cellY := cellAtScreen(focused, r, sx, sy)
		if n := a.tileAtCell(focused, cellX, cellY); n != nil {
			a.local(focused.ID).Selected = n.ID
		} else {
			a.clearSelected(focused.ID)
		}
		a.draw()
		a.scheduleURLUpdate()
		return nil

	case dragdrop.DropCreateTemplate:
		// Template-drag drop: turn the synthetic ghost into a real node by
		// asking the server to create it at the snapped cell.
		a.commitTemplateDrop(d, sx, sy)
		return nil

	case dragdrop.DropPanEnd:
		// Pan drag end: just persist viewport state (the URL now; the grid
		// framing via the draw()-armed settle persister).
		a.scheduleURLUpdate()
		a.draw()
		return nil

	case dragdrop.DropDelete:
		// Dropping the dragged tile on the bar slot's trashcan deletes it.
		// It resolves against that button, not the grid under the cursor, so
		// it works wherever the cursor happens to be.
		a.runDeleteTile(d, nil)
		a.ghost = nil
		a.draw()
		return nil

	case dragdrop.DropRejected:
		// No target, a forbidden cross-grid move, the same cell, or an
		// occupied one: snap back without a doomed round trip.
		a.cancelDragSnapBack(d)
		return nil

	case dragdrop.DropLink:
		// A cross-namespace left-drag: the destination gains a link and the
		// source stays put, because there is no cross-plugin move. The ghost
		// previewed this with the dashed chain badge.
		if a.ghost != nil {
			// The source was hidden for a would-be move; it stays — unhide it
			// now so the world reads "source intact + link appearing".
			a.ghost.hiddenTileID = ""
			a.ghost.hiddenPaneID = ""
		}
		a.landGhostAtCell(t, dropX, dropY)
		a.commitLinkDrop(d, t, dropX, dropY)
		a.draw()
		return nil
	}

	// DropMove: animate ghost to the snapped cell in the target grid's coords.
	a.landGhostAtCell(t, dropX, dropY)

	dstGridID := t.gridID
	srcGridID := d.srcGridID

	// A same-namespace left-drag is a move; a clone goes through the
	// right-drag path (commitRightClone in right_button.go) and never
	// reaches here. PlaceTile is the one placement writeback: an id plus the
	// full (grid, x, y, w, h) fact, with no descent path — the
	// well-into-own-subtree refusal is the server's own ancestor walk — and
	// no version claim, since placement is layout and last-writer-wins, with
	// the overlap check protecting the grid.
	req := &rpc.PlaceTileRequest{
		TileID: d.tileID,
		GridID: dstGridID,
		X:      dropX,
		Y:      dropY,
		W:      d.snapshotTile.W,
		H:      d.snapshotTile.H,
	}
	// A drag carries no parked value: the ghost is presentation, and snapping
	// it back to its origin is the honest reconcile the user can see.
	a.post(write{
		label: "PlaceTile", gid: srcGridID, alsoGID: dstGridID, refetchOnOK: true,
		call: func(ctx context.Context) error {
			_, err := a.cl.PlaceTile(ctx, req)
			return err
		},
		undo: func() { a.snapBackToOrigin(d) },
	})
	a.draw()
	return nil
}

// commitLinkDrop creates the link a cross-namespace left-drag drops: an exit
// well for a dragged well, with the same qualified child grid, framing, and
// label a + menu plugin-swatch drop produces, or a leaf link for a text, url,
// shell, or pane tile, whose link_target_id names the dragged tile — or, when
// the dragged tile is itself a leaf link, its target, so links never chain
// through middleman tiles. The source tile is not touched.
func (a *App) commitLinkDrop(d *dragState, t *dropTarget, dropX, dropY int64) {
	src := d.snapshotTile
	dstGridID := t.gridID
	if rpc.IsWellKind(src.Kind) {
		req := &rpc.CreateWellRequest{
			GridID: dstGridID, X: dropX, Y: dropY, W: src.W, H: src.H,
			ChildGridID: src.ChildGridID, Label: src.AltText,
			Framing: rpc.Framing{Cx: src.ViewCx, Cy: src.ViewCy, Zoom: src.ViewZoom},
		}
		a.postTileMutate("CreateWell", dstGridID, func(ctx context.Context) (*rpc.Tile, error) {
			return a.cl.CreateWell(ctx, req)
		}, nil)
		return
	}
	target := src.LinkTargetID
	if target == "" {
		target = src.ID
	}
	req := &rpc.CreateLeafLinkRequest{
		GridID: dstGridID, X: dropX, Y: dropY, W: src.W, H: src.H,
		Kind: src.Kind, LinkTargetID: target, Label: src.AltText,
	}
	a.postTileMutate("CreateLeafLink", dstGridID, func(ctx context.Context) (*rpc.Tile, error) {
		return a.cl.CreateLeafLink(ctx, req)
	}, nil)
}

// occupiedForDrop reports whether the dropped footprint (x, y, w, h) in
// gridID overlaps any cached tile other than excludeID. A move passes the
// dragged tile's own id, mirroring the server's PlaceTile self-exclusion, so
// the preflight cannot reject a placement the server would accept: a large
// tile dragged a short distance crosses its own old footprint, which is not a
// collision. A clone passes "", because the source tile is a real neighbor
// there.
func (a *App) occupiedForDrop(gridID string, x, y, w, h int64, excludeID string) bool {
	g, ok := a.c.Grid(gridID)
	if !ok {
		return false
	}
	for _, n := range g.Tiles {
		if n.ID == excludeID {
			continue
		}
		if dragdrop.RectsOverlap(n.X, n.Y, n.W, n.H, x, y, w, h) {
			return true
		}
	}
	return false
}

// landGhostAtCell lands the ghost on the drop target's cell (dropX, dropY):
// the target pane, the target's cell size, and the screen position of that
// cell. The one landing every drop commit uses — move, link, and clone — so
// the three cannot place the same drop differently.
func (a *App) landGhostAtCell(t *dropTarget, dropX, dropY int64) {
	a.landGhost(t.pane.ID, t.cellSize,
		t.originX+float64(dropX)*t.cellSize, t.originY+float64(dropY)*t.cellSize)
}

// landGhost is the one drop landing: the ghost belongs to paneID, drawn at
// that pane's cell size when cellSize > 0, and snaps to the screen cell
// (toX, toY).
func (a *App) landGhost(paneID string, cellSize, toX, toY float64) {
	if a.ghost != nil {
		a.ghost.paneID = paneID
		if cellSize > 0 {
			a.ghost.targetCellSize = cellSize
		}
	}
	a.startSnap(toX, toY, snapMs)
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
// back to the original location of the dragged tile. Used both for failed
// server commits and for drops outside any pane. Position animates on
// the anim.Animation; the ghost's displayedCellSize is independently
// lerped toward srcCellSize each frame so the ghost grows or shrinks
// back to its original size as it returns.
func (a *App) snapBackToOrigin(d *dragState) {
	if a.ghost == nil {
		return
	}
	a.ghost.paneID = d.originPaneID
	if d.srcCellSize > 0 {
		a.ghost.targetCellSize = d.srcCellSize
	}
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
	if hit.Kind == rpc.KindURL && hit.URLString == "" && hit.LinkTargetID == "" {
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
	a.descend(target, hit, nil)
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

// descentTextMode applies textedit.DescentMode (the one owner) to a
// cached tile row at descent; cursorURL is the restore path's extra
// input (an address that encodes a text cursor).
func (a *App) descentTextMode(file *rpc.Tile, cursorURL bool) string {
	return textedit.DescentMode(textedit.ModeInput{
		Kind: file.Kind, ServesPage: file.ServesPage, ReadOnly: a.tileReadOnly(file),
		Cached: true, CursorURL: cursorURL, Stored: file.TextMode,
	})
}

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

// autoLiveOnRestore is autoLiveOnDescent for the restore paths: a reload
// (applyURLState), a pane-tile install, or an ascent landing back on a
// content frame stacked under a deeper visit (landOnFrame). The tile row is
// not necessarily cached at restore time, so it is fetched first, and the
// pane is re-resolved when the read lands, since the user may have moved on
// and where they went is never overridden.
func (a *App) autoLiveOnRestore(paneID, tileID string) {
	go func() {
		tile, err := a.cl.GetTile(context.Background(), tileID)
		if err != nil {
			return // a leaf whose reference no longer resolves stays frozen
		}
		a.c.UpdateTile(tile.GridID, *tile)
		a.healStalePanePath(paneID, tile)
		a.autoLiveOnDescent(paneID, tile)
		a.draw()
	}()
}

// healStalePanePath re-derives a restored pane's path when its stored
// (anchor, path) no longer leads to the descended tile's grid: the tile moved,
// since its id is immutable but its path is not. A scoped `id:` Search, the
// one find verb, answers with the current containing-well chain; the pane
// re-anchors at the owning root with the fresh path, so the descent binds and
// the crumbs show a true path from the root. The layout persister derives the
// corrected layout from the live tree on its next tick, so the heal persists
// with no dedicated writer. It runs on the restore goroutine, where a
// blocking read is fine; an unsearchable tile, from a plugin without Search,
// stays on its frozen preview.
func (a *App) healStalePanePath(paneID string, tile *rpc.Tile) {
	fp := a.tree.FindPane(paneID)
	if !pane.StillDescended(fp, tile.ID) {
		return
	}
	if a.isEphemeralTile(fp, tile) {
		// An ephemeral descent is deliberately focused off the pane's grid:
		// the scratch tile rides above whatever place the pane frames. The
		// path is not stale — the tile is elsewhere by design — and healing
		// would re-anchor the pane into the scratch grid.
		return
	}
	if a.gridIDForPathFrom(fp.Anchor(), fp.Path()) == tile.GridID {
		return
	}
	res, err := a.cl.Search(context.Background(), "id:"+tile.ID, tile.ID, 1)
	if err != nil || len(res) == 0 {
		return
	}
	wells := res[0].Path
	fp = a.tree.FindPane(paneID)
	if fp == nil || fp.ContentID() != tile.ID {
		return // the user moved on while the locate was in flight
	}
	anchor := tile.GridID
	path := make([]string, 0, len(wells))
	if len(wells) > 0 {
		anchor = wells[0].GridID
		for _, w := range wells {
			path = append(path, w.ID)
		}
	}
	fp.Stack = pane.StackAt(anchor, path, tile.ID)
	// Center the healed viewport on the tile in its new grid, so ascending
	// out of the descent lands looking at the tile, not a stale offset.
	fp.Cx = float64(tile.X) + float64(tile.W)/2
	fp.Cy = float64(tile.Y) + float64(tile.H)/2
	a.fetchGrid(tile.GridID)
	a.draw()
	a.scheduleURLUpdate()
}

// autoLiveOnDescent applies the shellconn.DecideAutoLive verdict for the
// just-descended tile: open the url view, attach or create the shell PTY,
// probe an unknown shell session first, or stay frozen — text, browser hosts,
// dead sessions. It is the one auto-live owner, and the refresh affordances
// are the retry for the cases it stays frozen on. tile is the descent-time
// row, passed by value, because an ephemeral scratch tile is in no cached
// grid.
func (a *App) autoLiveOnDescent(paneID string, tile *rpc.Tile) {
	tileID := tile.ID
	fp := a.tree.FindPane(paneID)
	if !pane.StillDescended(fp, tileID) {
		return
	}
	// The shell facts key by the content id, so a link attaches its target's
	// session: the same reads shellRefreshButtonVisible does, so the two
	// decisions cannot disagree about a dead session.
	cid := tile.ContentID()
	alive, known := a.shellAlive[cid]
	verdict := shellconn.DecideAutoLive(
		tile.WebContent(), tile.Kind == rpc.KindShell,
		a.caps.LiveURL, a.caps.LiveShell,
		tile.PreviewBlobID != 0, known, alive, tile.URLFrozen)
	switch verdict {
	case shellconn.AutoLiveURL:
		a.openURLStream(fp, tileID)
	case shellconn.AutoLiveShell:
		a.openShellStream(fp, tileID)
	case shellconn.AutoLiveProbeShell:
		a.probeShellSessionAlive(cid, func(nowAlive bool) {
			// Re-check that the pane is still in this descent when the
			// verdict lands: the probe is async and the user may have moved
			// on.
			if p := a.tree.FindPane(paneID); nowAlive && p != nil && p.ContentID() == tileID {
				a.openShellStream(p, tileID)
			}
		})
	}
}

// saveTextBeforeAscent posts the editor buffer (if text mode is active)
// and the framed window back to the server, through the dispatcher: a
// failure reacts via clientsync (transport parks in the outbox, a verdict
// refetches and surfaces) like every other mutation.
func (a *App) saveTextBeforeAscent(p *pane.Pane, file rpc.Tile) {
	// SetTextView, and the framed-window cache patch, are text-tile
	// concerns: url and shell tiles carry no text framing, and the server's
	// SetTextView rejects non-text kinds with InvalidArgument, which would
	// surface as an error the user has to read. A serves_page descent is web
	// content and carries no text framing either.
	if file.Kind != rpc.KindText || file.ServesPage {
		return
	}
	gid := a.gridIDForPane(p)
	r := paneRectFor(a, p)
	scrollX := int64(p.TextScrollX + 0.5)
	scrollY := int64(p.TextScrollY + 0.5)

	// Content: read the tile's own cache entry, and only when it carries an
	// unsaved edit. Never the DOM. Reading the singleton textarea here would
	// attribute its bytes to whatever tile this pane points at, so a bulk
	// flush — a pane collapse, a level boundary — over a pane the singleton
	// was not bound to would save one document's bytes as another's content.
	// Posting unconditionally would also make a merely-opened tile rewrite
	// its blob and bump its version on every visit; dirty-gating keeps a
	// pure read write-free.
	// Read-only host tiles have no write-back at all: no content, since the
	// body is derived, and no framing store either — the fs plugin's SetTile
	// refuses text framing, so posting SetTextView from here would only
	// manufacture an error strip. Their mode and scroll stay session
	// facts.
	if a.tileReadOnly(&file) {
		return
	}
	buf, hasBuf := a.c.DirtyContent(file.ContentID())

	// The framed window in doc px: scroll position + the inner box size
	// (= screen px, since scale is fixed at 1.0). The parent-grid preview
	// crops this rectangle out of the re-rendered doc.
	_, _, iw, ih := textInnerBox(r)
	viewW := int64(iw + 0.5)
	viewH := int64(ih + 0.5)

	// Patch the cache immediately so the ascent transition (and any other
	// pane previewing this tile) reflects the framed window + mode before
	// the server round-trip lands.
	patched := file
	patched.TextX = scrollX
	patched.TextY = scrollY
	patched.TextW = viewW
	patched.TextH = viewH
	patched.TextMode = p.TextMode
	a.c.Apply(rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: patched}})

	mode := p.TextMode
	// Through the per-tile save queue: a debounced keystroke save may still
	// be in flight, and this flush claims a version too, so the queue
	// serializes them and the version is read at send time.
	a.textSaves.Enqueue(file.ID, func() {
		curVersion := file.Version
		if g, ok := a.c.Grid(gid); ok {
			if f, ok := g.Tiles[file.ID]; ok {
				curVersion = f.Version
			}
		}
		// Update the content first if the user was editing. The content
		// write claims the save basis — the version of the bytes the entry
		// derives from — never the row version read above: a foreign
		// writer's event advances the row without this client seeing the new
		// bytes, and claiming it would save the stale entry right over the
		// foreign edit. A stale basis conflicts at the server and reconciles
		// visibly instead.
		if hasBuf {
			// The write addresses the content owner, so a link's doc saves
			// under its target's id, as flushTileContent does. The row
			// version is a valid fallback only when this row is the owner.
			cid := file.ContentID()
			saveVersion := curVersion
			if file.ID != cid {
				saveVersion = 0
			}
			if base, ok := a.c.SaveBasis(cid); ok {
				saveVersion = base
			}
			tile, ok := a.postWriteContent(gid, cid, saveVersion, buf)
			if !ok {
				return
			}
			if file.ID == cid {
				// The SetTextView below claims this row's version, so only
				// advance it when the content write bumped this same row: a
				// link's target version is a different row's fact.
				curVersion = tile.Version
			}
		}
		// Persist the framed window and mode so re-descent and the preview
		// show it however the user left it across reloads, and only when
		// something changed (textedit.FramingChanged, the one rule): a pure
		// descend-and-ascent must not write.
		next := textedit.Framing{X: scrollX, Y: scrollY, W: viewW, H: viewH, Mode: mode}
		if !textedit.FramingChanged(textedit.FramingOf(file), next) {
			return
		}
		req := &rpc.SetTextViewRequest{
			TileID:   file.ID,
			TextX:    scrollX,
			TextY:    scrollY,
			TextW:    viewW,
			TextH:    viewH,
			TextMode: mode,
		}
		a.do(write{
			label: "SetTextView", gid: gid, id: file.ID, refetchOnOK: true,
			call: func(ctx context.Context) error {
				_, err := a.cl.SetTextView(ctx, req)
				return err
			},
			beacon: func() (string, []byte, string) {
				path, body := rpc.SetTextViewBeacon(req)
				return path, body, rpc.BeaconJSONType
			},
		})
	})
}

// startPaletteDrag arms a drag from the i'th palette item. The dragState is
// set up so the existing ghost machinery treats it like a regular tile drag
// (snapshot tile + cell offset), but with isTemplate=true so onMouseUp
// branches to creation/mount instead of move. The palette stays open during
// the drag — it'll close on commit. For a plugin item the release path also
// distinguishes a click (enter the plugin) from a drag (mount a link).
func (a *App) startPaletteDrag(p *pane.Pane, r pane.Rect, idx int, sx, sy float64) {
	items := a.paletteItems(p)
	if idx < 0 || idx >= len(items) {
		return
	}
	item := items[idx]
	tx, ty, tw, _ := a.paletteTileRect(p, idx)
	a.dragging = &dragState{
		originPaneID:  p.ID,
		originFocused: true, // the palette only opens on the focused pane
		isTemplate:    true,
		item:          item,
		// The menu belongs to the pane's node: the drop rules compare this
		// against the destination's node.
		menuNS:        a.paneNodeNS(p),
		startScreenX:  sx,
		startScreenY:  sy,
		curScreenX:    sx,
		curScreenY:    sy,
		cellOffsetX:   0.5,
		cellOffsetY:   0.5,
		snapshotTile:  paletteItemGhostNode(item),
		originScreenX: tx,
		originScreenY: ty,
		// Start the ghost at the (fixed, zoom-independent) swatch size; the
		// drop-target machinery grows/shrinks it to the destination grid's
		// cell size as the cursor moves over a pane, and back to the swatch
		// size when off-grid — same lerp as dragging a tile across wells.
		srcCellSize: tw,
	}
}

// paletteItemGhostNode synthesizes a 1×1 rpc.Tile matching the palette item,
// so the ghost renderer can paint the in-flight tile using the same drawNode
// path that a real tile would use. A plugin item is the shared synthetic
// exit well (rpc.PluginWellTile) with the plugin's uuid as its id, so the
// health tint and the broken/rootless descent guard can name the plugin.
func paletteItemGhostNode(item paletteItem) rpc.Tile {
	if item.isPlugin {
		t := rpc.PluginWellTile(item.plugin)
		t.ID = item.plugin.UUID
		return t
	}
	if pr, ok := primitiveFor(item.primitive); ok {
		return pr.ghost
	}
	return rpc.Tile{}
}

// commitTemplateDrop resolves the template drag at release. Off-pane
// or overlap → snap-back, palette stays open. Valid drop → for url/
// upload, prompt first; on confirm, fire the create RPC at the
// snapped cell. On any successful commit, the palette closes.
func (a *App) commitTemplateDrop(d *dragState, sx, sy float64) {
	destPane, destRect, ok := a.paneAtScreen(sx, sy)
	if !ok {
		a.cancelDragSnapBack(d)
		return
	}
	dpscreen := paneToDragdrop(destPane, destRect)
	dcx, dcy := dpscreen.ScreenToCell(sx, sy)
	dropX := dragdrop.SnapToCell(dcx - d.cellOffsetX)
	dropY := dragdrop.SnapToCell(dcy - d.cellOffsetY)

	// Bail early if the drop cell would overlap an existing node.
	if a.tileAtCell(destPane, dropX, dropY) != nil {
		a.cancelDragSnapBack(d)
		return
	}

	// A plugin item dropped into the destination grid becomes an exit-well
	// link to its root grid. A connection row drops the same way, its
	// chained root already qualified; links are the cross-boundary
	// vocabulary. Only writable grids accept it, and anything else snaps
	// back.
	if d.item.isPlugin {
		droppable := pluginhealth.Classify(d.item.plugin) == pluginhealth.Enterable
		if !droppable || !a.gridWritable(a.gridIDForPane(destPane)) {
			a.cancelDragSnapBack(d)
			return
		}
		targetX, targetY := dpscreen.CellToScreen(float64(dropX), float64(dropY))
		a.landGhost(destPane.ID, 0, targetX, targetY)
		a.createPluginLinkAtCell(destPane, d.item.plugin, dropX, dropY)
		a.menu.Close()
		return
	}

	// A primitive belongs to the node whose menu offered it: the swatch was
	// gated by that node's grids and policy, and creating a remote node's
	// text tile inside a local grid is a category error. Same-node drops
	// only; a cross-node drop refuses visibly and snaps back.
	if a.paneNodeNS(destPane) != d.menuNS {
		a.reportErr(errsurface.Info, "menu",
			"this menu belongs to another node — drop into a grid on that node, or open the menu here")
		a.cancelDragSnapBack(d)
		return
	}

	// Every template commits immediately with the snap-and-create gesture.
	// The drop never prompts: whatever a kind needs to be useful is asked
	// for on the first descent, so create is one experience everywhere —
	// drop, descend, fill in.
	targetX, targetY := dpscreen.CellToScreen(float64(dropX), float64(dropY))
	a.landGhost(destPane.ID, 0, targetX, targetY)

	// A promote drag is the one arm that is not a plain create: the ephemeral
	// url dragged off the bar's crumb becomes a persistent tile carrying its
	// address, and the pane relocates onto it. Every other drop is the
	// primitives table's create.
	if d.item.primitive == tplURL && d.item.promotePane != "" {
		a.promoteEphemeralURL(d.item.promotePane, destPane, dropX, dropY)
	} else if pr, ok := primitiveFor(d.item.primitive); ok {
		pr.create(a, destPane, dropX, dropY)
	}
	a.menu.Close()
}

// createPluginLinkAtCell fires CreateWell with the plugin's qualified root
// grid as the child: an exit-well link, through CreateTile, the one create.
// The link's framing seeds from the plugin's persisted root view, so its
// preview shows what descent will show.
func (a *App) createPluginLinkAtCell(p *pane.Pane, pl rpc.PluginInfo, cellX, cellY int64) {
	gid := a.gridIDForPane(p)
	req := &rpc.CreateWellRequest{
		GridID: gid, X: cellX, Y: cellY, W: 1, H: 1,
		ChildGridID: pl.RootGridID,
		Label:       pl.Label,
		Framing:     rpc.Framing{Cx: pl.RootViewCx, Cy: pl.RootViewCy, Zoom: pl.RootViewZoom},
	}
	a.postTileMutate("CreateWell", gid, func(ctx context.Context) (*rpc.Tile, error) {
		return a.cl.CreateWell(ctx, req)
	}, nil)
}

// createWellAtCell fires CreateWell at the given cell. The footprint is 1×1
// and the well is created unnamed; naming happens from inside, through the
// bar title.
func (a *App) createWellAtCell(p *pane.Pane, cellX, cellY int64) {
	gid := a.gridIDForPane(p)
	req := &rpc.CreateWellRequest{
		GridID: gid, X: cellX, Y: cellY, W: 1, H: 1,
	}
	a.postTileMutate("CreateWell", gid, func(ctx context.Context) (*rpc.Tile, error) {
		return a.cl.CreateWell(ctx, req)
	}, nil)
}

// createTextAtCell fires CreateText at the given cell with the given
// initial bytes. Footprint is 1×1.
func (a *App) createTextAtCell(p *pane.Pane, data []byte, cellX, cellY int64) {
	gid := a.gridIDForPane(p)
	req := &rpc.CreateTextRequest{
		GridID: gid, X: cellX, Y: cellY, W: 1, H: 1,
		Data: data,
	}
	a.postTileMutate("CreateText", gid, func(ctx context.Context) (*rpc.Tile, error) {
		return a.cl.CreateText(ctx, req)
	}, nil)
}

// createURLAtCell fires CreateURL at the given cell, address-less: the tile
// lands inert, and the first descent prompts for the address
// (openConfigureURL) and writes it as the tile's content.
func (a *App) createURLAtCell(p *pane.Pane, cellX, cellY int64) {
	gid := a.gridIDForPane(p)
	req := &rpc.CreateURLRequest{
		GridID: gid, X: cellX, Y: cellY, W: 1, H: 1,
	}
	a.postTileMutate("CreateURL", gid, func(ctx context.Context) (*rpc.Tile, error) {
		return a.cl.CreateURL(ctx, req)
	}, nil)
}

// openConfigureURL prompts for a bare url tile's address on its first
// descent, reusing the url modal with its visited-url suggestions. Submitting
// writes the address as the tile's content — the store's url arm: versioned,
// validated, bumping — and then descends, so the fill-in flows straight into
// the page. Every descent goes live, so there is no special go-live
// handling.
func (a *App) openConfigureURL(p *pane.Pane, t *rpc.Tile) {
	gid := a.gridIDForPane(p)
	paneID, id := p.ID, t.ID
	// The address is content, so this write claims a version like every
	// content write: the row as the descent saw it, which is exactly the
	// value the user is filling in.
	version := t.Version
	candidates := a.urlSuggestCandidates(uuidOf(gid))
	a.openURLModal(candidates, func(url string) {
		go func() {
			// Through the plain dispatcher, not postWriteContent: the typed
			// url has no cache entry backing it, since the modal is the only
			// holder, so the content path's "the dirty entry is the record"
			// rule cannot cover it. The dispatcher parks the closure itself
			// on a transport failure and the address lands on the retry
			// kick; only the descent is skipped.
			var tile rpc.Tile
			err := a.do(write{
				label: "ConfigureURL", gid: gid, id: id,
				source: "url", failText: "url save failed",
				call: func(ctx context.Context) error {
					t, werr := a.cl.WriteContent(ctx, id, version, []byte(url))
					if werr == nil {
						tile = *t
					}
					return werr
				},
			})
			if err != nil {
				return
			}
			a.c.UpdateTile(tile.GridID, tile)
			fp := a.tree.FindPane(paneID)
			if fp == nil || fp.ContentID() != "" {
				return
			}
			a.descend(fp, &tile, nil)
			a.draw()
		}()
	}, func() {
		a.draw()
	})
}

// scratchGridForPane returns the qualified scratch grid id where ephemeral
// visits from the pane's grid land. Every grid a node serves carries one —
// the owning plugin's own, or the serving node's home scratch grid when the
// plugin declares none — so "" means only an uncached grid whose plugin is
// not local.
func (a *App) scratchGridForPane(p *pane.Pane) string {
	gid := a.gridIDForPane(p)
	// The fact rides on the grid (Grid.ScratchGridID, stamped by the serving
	// node and chained through mounts): a local plugin-list lookup cannot
	// answer for a remote plugin behind a transit mount.
	if g, ok := a.c.Grid(gid); ok && g.Meta.ScratchGridID != "" {
		return g.Meta.ScratchGridID
	}
	// Fallback for an uncached grid: the local plugin list, matching on the
	// first segment, so local plugins only.
	want := uuidOf(gid)
	for _, pl := range a.plugins {
		if pl.UUID == want {
			return pl.ScratchGridID
		}
	}
	return ""
}

// scratchOrReport is scratchGridForPane with the failure surfaced: a visit
// with nowhere to land must say so, or the click looks like it just did
// nothing. Every ephemeral-visit entry point asks here, so no path can fail
// silently.
func (a *App) scratchOrReport(p *pane.Pane) string {
	s := a.scratchGridForPane(p)
	if s == "" {
		a.reportErr(errsurface.Error, "ephemeral",
			"nowhere to open an ephemeral visit: this grid carries no scratch grid")
	}
	return s
}

// visitEphemeralURL creates an ephemeral url tile in the current plugin's
// scratch grid (off any visible grid) and descends into it, going live —
// "descend into a url" from the menu's url swatch (clicked, not dragged). The
// tile lives only in the scratch grid (visited-url history that feeds
// autocomplete; a resolvable deep-link), so ascent returns to where you were
// and leaves nothing on the grid. Mirrors createURLAtCell's auto-go-live, but
// descends WITHOUT re-anchoring — the pane keeps its grid and just focuses the
// off-grid tile, which render / url stream / ascent resolve by id (descendedTile).
func (a *App) visitEphemeralURL(p *pane.Pane, url string) {
	scratch := a.scratchOrReport(p)
	if scratch == "" {
		return
	}
	paneID := p.ID
	req := &rpc.CreateURLRequest{
		GridID: scratch, X: 0, Y: 0, W: 1, H: 1, URL: url,
	}
	a.postTileMutate("CreateURL", scratch, func(ctx context.Context) (*rpc.Tile, error) {
		return a.cl.CreateURL(ctx, req)
	}, func(tile rpc.Tile) {
		if fp := a.tree.FindPane(paneID); fp != nil {
			a.descend(fp, &tile, nil)
		}
	})
}

// isEphemeralTile reports whether t is an ephemeral (scratch-grid) tile of the
// plugin pane p sits in. It is the one client-side derivation of "ephemeral":
// the tile's grid is the plugin's scratch grid. Both facts are server-owned —
// Grid.ScratchGridID rides on the grid, chained through mounts — so no new
// wire field is needed. Ephemeral tiles are deleted on ascent, and the
// descended pane border goes gray to say so.
func (a *App) isEphemeralTile(p *pane.Pane, t *rpc.Tile) bool {
	s := a.scratchGridForPane(p)
	return s != "" && t.GridID == s
}

// leavingEphemeral is the decision that a pane leaving tile t deletes it: the
// tile is ephemeral and no other pane still shows it (pane.OtherPaneShows,
// since a split clones the visit). Every ascent-shaped path — the animated
// ascent, the instant pop, promotion onto a grid — asks here, so no
// path can forget the guard.
func (a *App) leavingEphemeral(p *pane.Pane, t *rpc.Tile) bool {
	return a.isEphemeralTile(p, t) && !a.tree.OtherPaneShows(p.ID, t.ID)
}

// deleteEphemeralTile removes an ascended-from ephemeral tile — gray means
// gone. The row is deleted, and for a shell the plugin kills its tmux
// session, and all its processes, as part of the delete. A failure surfaces
// on the strip; otherwise the tile would silently leak in the scratch grid
// until the startup sweep.
func (a *App) deleteEphemeralTile(tileID string) {
	go func() {
		// No claim: a delete is the user's explicit action, and the stream
		// close that precedes it triggers the plugin's detach-time title
		// capture. Captures do not bump the row and a delete carries no
		// claim, so the two cannot race.
		err := a.cl.DeleteTile(context.Background(), &rpc.DeleteTileRequest{
			TileID: tileID,
		})
		if err != nil {
			a.reportErr(errsurface.Error, "ephemeral",
				"ephemeral tile cleanup failed: "+rpcErrText(err))
		}
	}()
}

// visitEphemeralShell creates an ephemeral shell tile in the current plugin's
// scratch grid and descends into it, spawning the PTY: "open a shell" from
// the menu's shell swatch, clicked rather than dragged. The shell twin of
// visitEphemeralURL, with the opposite exit contract — ascent deletes the
// tile and its tmux session, which the gray border warns about.
func (a *App) visitEphemeralShell(p *pane.Pane) {
	scratch := a.scratchOrReport(p)
	if scratch == "" {
		return
	}
	paneID := p.ID
	req := &rpc.CreateShellRequest{GridID: scratch, X: 0, Y: 0, W: 1, H: 1}
	a.postTileMutate("CreateShell", scratch, func(ctx context.Context) (*rpc.Tile, error) {
		return a.cl.CreateShell(ctx, req)
	}, func(tile rpc.Tile) {
		if fp := a.tree.FindPane(paneID); fp != nil {
			a.descend(fp, &tile, nil)
		}
	})
}

// openLinkBelow handles a link opened out of a live tile: a new-window intent
// from a live url view (target=_blank, window.open, ctrl or cmd-click) and a
// url activated in a live shell. It splits the pane horizontally and opens the
// url as an ephemeral visit in the new lower half, so the link renders next to
// the page or terminal it came from, on the same session, and dies on ascent
// like every ephemeral visit. If the split fails, on a degenerate pane, the
// visit opens in place instead: the link is never silently dropped.
func (a *App) openLinkBelow(paneID, url string) {
	p := a.tree.FindPane(paneID)
	if p == nil {
		return
	}
	// The visit needs somewhere to land before anything splits: without a
	// scratch grid the split would only birth a pane with nothing to show.
	if a.scratchOrReport(p) == "" {
		return
	}
	// SplitOnSideAt splits the focused pane, and the link's pane may not be
	// it, since a background page can call window.open. Focus it first,
	// which is also where the user's attention is about to go.
	a.focusToPane(p)
	newP := a.splitBelowForOpen(p)
	if newP == p {
		a.visitEphemeralURL(p, url)
		return
	}
	a.draw()
	a.scheduleURLUpdate()
	a.visitEphemeralURL(newP, url)
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
// the grid containing the tile. The ephemeral delete-on-ascent is guarded
// by leavingEphemeral, so that ascent never deletes the tile the source
// pane still shows.
func (a *App) splitBelowForOpen(p *pane.Pane) *pane.Pane {
	if !pane.CanSplit(pane.SideBottom, paneRectFor(a, p)) {
		return p
	}
	newP, err := a.tree.SplitOnSideAt(pane.SideBottom, 0.5)
	if err != nil {
		return p
	}
	if newP.ContentID() != "" {
		a.ascend(newP, 1, true)
	}
	return newP
}

// shellURLActivate handles a click on an http(s) url in a live shell, the
// xterm link provider's activate: open it below, exactly like a link a live
// url view pops, so links out of live tiles have one behavior. A stacked
// visit is just another frame on the pane's place. A no-op if the shell is no
// longer the pane's active descent.
func (a *App) shellURLActivate(paneID, url string) {
	if p := a.tree.FindPane(paneID); p != nil && p.ContentID() != "" {
		a.openLinkBelow(paneID, url)
	}
}

// createShellAtCell fires CreateShell at the given cell. The first descent
// creates the tile's private tmux session; a later ascent shows the frozen
// JPEG, and re-descending reattaches to the same session with its state
// preserved.
func (a *App) createShellAtCell(p *pane.Pane, cellX, cellY int64) {
	gid := a.gridIDForPane(p)
	req := &rpc.CreateShellRequest{
		GridID: gid, X: cellX, Y: cellY, W: 1, H: 1,
	}
	// The drop just lands the tile, with no auto-descent, like every other
	// primitive. The first descent creates the session, through
	// DecideAutoLive's fresh-shell arm, which fires when there is no preview
	// blob.
	a.postTileMutate("CreateShell", gid, func(ctx context.Context) (*rpc.Tile, error) {
		return a.cl.CreateShell(ctx, req)
	}, nil)
}

// mouseXY returns the click coordinates relative to the canvas.
func mouseXY(ev js.Value, canvas js.Value) (float64, float64) {
	rect := canvas.Call("getBoundingClientRect")
	x := ev.Get("clientX").Float() - rect.Get("left").Float()
	y := ev.Get("clientY").Float() - rect.Get("top").Float()
	return x, y
}

// (Right-button gesture handling lives in right_button.go.)

// promoteEphemeralURL turns the ephemeral url visit shown in pane
// originPaneID into a persistent url tile at (cellX, cellY) of destPane's
// grid: the bar crumb dragged onto a grid. The tile is created with the
// visit's current address, since the page may have navigated, and
// finishPromote then moves the visit onto it.
func (a *App) promoteEphemeralURL(originPaneID string, destPane *pane.Pane, cellX, cellY int64) {
	op := a.tree.FindPane(originPaneID)
	if op == nil {
		return
	}
	t, ok := a.descendedTile(op)
	if !ok || t.Kind != rpc.KindURL || !a.isEphemeralTile(op, &t) {
		return
	}
	url := t.URLString
	if v := a.urlViewFor(op.ID); v != nil && v.lastURL != "" {
		url = v.lastURL
	}
	gid := a.gridIDForPane(destPane)
	destID := destPane.ID
	oldID := t.ID
	req := &rpc.CreateURLRequest{GridID: gid, X: cellX, Y: cellY, W: 1, H: 1, URL: url}
	a.postTileMutate("CreateURL", gid, func(ctx context.Context) (*rpc.Tile, error) {
		return a.cl.CreateURL(ctx, req)
	}, func(created rpc.Tile) {
		a.finishPromote(originPaneID, destID, oldID, created)
	})
}

// finishPromote moves the live visit from the ephemeral row onto the
// persistent tile just created: the view's final frame, title, and trail
// freeze onto the new tile, never the row about to die; the ephemeral row is
// deleted, unless a split sibling still shows it; the pane relocates to the
// new tile's grid (pane.RelocateTo, so the nav chain and the next ascent read
// the new place, with the destination pane's ascent viewport); and the page
// goes live again on the new tile.
func (a *App) finishPromote(originPaneID, destPaneID, oldID string, created rpc.Tile) {
	op := a.tree.FindPane(originPaneID)
	dp := a.tree.FindPane(destPaneID)
	if !pane.StillDescended(op, oldID) || dp == nil {
		return // moved on mid-flight: the tile stays where it was dropped
	}
	a.closeURLStreamTo(op.ID, &freezeTarget{tileID: created.ID, gridID: created.GridID}, true)
	// The row dies only if no sibling pane still shows the visit; a split
	// clone keeps it and deletes it on its own ascent. The same guard every
	// ascent applies, through the same door.
	if old := a.cachedTileByID(oldID); old != nil && a.leavingEphemeral(op, old) {
		a.deleteEphemeralTile(oldID)
	}
	// The pane follows its content: RelocateTo replaces the visit's frame
	// with one on the destination's stack, so the next ascent lands where the
	// tile now lives. There is no separate saved viewport to keep in step.
	op.RelocateTo(dp, created.ID)
	a.placeURLView(op.ID, created)
	a.refreshFileOverlay()
	a.scheduleURLUpdate()
	a.draw()
}
