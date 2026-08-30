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
// the canvas (a gesture can only START there), move/release at the window so
// an in-flight gesture survives whatever the pointer crosses. Gridwell
// navigation is strictly mouse-only: every gesture (move, descend,
// ascend, resize, clone, delete) is reachable via left/right click,
// drag, and the scroll wheel — there are no navigation keybindings.
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
	// move/up listen at the WINDOW, capture phase — never on the canvas. A
	// gesture armed by a canvas mousedown must keep tracking no matter what
	// the pointer crosses: a text descent floats a DOM overlay (textarea /
	// rendered view) above the canvas, so a fast divider drag whose next
	// mousemove jumped into that rect hit-targeted the overlay and a
	// canvas-scoped listener heard neither the move nor the release — the
	// drag wedged, and it stayed armed after the button was let go (the
	// "can't drag a text pane border fast" bug). Capture also beats any
	// stopPropagation an overlay library (xterm) might do. When no gesture
	// is in flight, events that didn't target the canvas are ignored, so
	// overlay-local behavior (typing, selection, terminal input) is
	// exactly as before.
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
	// Ctrl/Cmd +/-/0 zooms a descended tile's CONTENT (issue #82) — checked
	// first so Electron's built-in page zoom never double-fires.
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
// inside the given pane. Uses floor (which cell is the cursor *in?*), not
// round — round-half made clicks in the lower-right half of a tile miss.
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
// This is the SINGLE focus-transfer owner — call it from every press path:
// canvas onMouseDown, onForwardedRightDown, onForwardedLeftDown. Calling it
// is always safe even when focus has not moved (no-op on same pane).
func (a *App) focusToPane(p *pane.Pane) bool {
	prev := a.tree.Focus
	_ = a.tree.SetFocus(p.ID)
	if !a.menu.TransferFocus(prev, a.tree.Focus) {
		return false
	}
	// Focus moved → file-mode chrome must follow. The textarea overlay only
	// ever lives over the focused pane, so without this call a click on a
	// sibling pane in text mode leaves the textarea stranded.
	a.refreshFileOverlay()
	a.draw()
	return true
}

func (a *App) onWheel(this js.Value, args []js.Value) any {
	args[0].Call("preventDefault")
	dy := args[0].Get("deltaY").Float()
	sx, sy := mouseXY(args[0], a.canvas)
	p, r, ok := a.paneAtScreen(sx, sy)
	if !ok {
		return nil
	}
	// Wheel over a pane's BAR band zooms that pane as if the cursor were
	// at the pane's center (issue #220; every pane wears a band since
	// #267): the escape hatch for a grid tiled wall-to-wall with wells,
	// where every content position claims the well-zoom (#210) and no
	// empty spot remains.
	if bx, top, bw, barOK := a.bottomBarRectFor(p); barOK &&
		sy >= top && sy < top+wsbar.RowH && sx >= bx && sx < bx+bw {
		if p.ContentID() == "" {
			a.wheelZoomPaneAt(p, r, dy, r.X+r.W/2, r.Y+r.H/2)
		}
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
		InContentBox:      pointInLiveContent(r, sx, sy),
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
		// Text file: fixed scale, no zoom — the wheel scrolls the rendered
		// window vertically. (Text mode: the textarea scrolls itself and the
		// event never reaches the canvas.)
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
		// Issue #210: the wheel zooms the grid IN the hovered well — its
		// stored view_* preview framing, the one owner the renderer reads
		// per frame — not the grid the pane shows. Pure math in
		// zoomtrans.WellWheelView (the same cursor-anchored kernel as the
		// pane zoom, in preview space); the cache is patched per notch and
		// the settle persister posts one framing write per tile at flush.
		ps := paneToDragdrop(p, r)
		x0, y0 := ps.CellToScreen(float64(hoverWell.X), float64(hoverWell.Y))
		x1, _ := ps.CellToScreen(float64(hoverWell.X)+1, float64(hoverWell.Y))
		parentCell := x1 - x0
		wpx := float64(hoverWell.W) * parentCell
		hpx := float64(hoverWell.H) * parentCell
		zw := zoomtrans.Well{
			X: hoverWell.X, Y: hoverWell.Y, W: hoverWell.W, H: hoverWell.H,
			ViewCx: hoverWell.ViewCx, ViewCy: hoverWell.ViewCy, ViewZoom: hoverWell.ViewZoom,
		}
		// The float center accumulates across the burst (issue #219): first
		// notch seeds from the stored center; later notches feed the drift
		// back in, so cursor-anchored zoom actually travels.
		cx0, cy0 := hoverWell.ViewCx, hoverWell.ViewCy
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
// point under the anchor stays under it after the zoom — like every map
// app. The bar-band wheel (issue #220) calls this with the pane's center.
func (a *App) wheelZoomPaneAt(p *pane.Pane, r pane.Rect, dy, sx, sy float64) {
	ps := paneToDragdrop(p, r)
	cellX, cellY := ps.ScreenToCell(sx, sy)
	// Step clamp + cursor-anchored re-center is the pure zoomtrans.WheelZoom.
	p.Zoom, p.Cx, p.Cy = zoomtrans.WheelZoom(dy, p.Zoom, p.Cx, p.Cy, cellX, cellY, zoomFactor, zoomMin, zoomMax)
	a.draw()
	a.scheduleURLUpdate()
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
	// The bottom bar: the focused pane's bottom band (#220). LEFT-click a
	// workspace crumb LEAVES it (and everything deeper) — the one gesture
	// that crosses the workspace boundary; RIGHT-click renames. Chain-crumb
	// left-clicks ascend (#222).
	if a.bottomBarClick(sx, sy, args[0].Get("button").Int()) {
		args[0].Call("preventDefault")
		return nil
	}
	// A left-click inside the open palette belongs to the palette — routed
	// BEFORE pane resolution, because the popover (anchored to the bar slot,
	// issue #214) floats over whatever pane happens to be under it, and
	// resolving that pane first would transfer focus and close (SyncFocus)
	// the very menu being used. Landing on a swatch starts a template drag;
	// missing one swallows the click so the popover stays open.
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
	// focusToPane transfers focus, closes the menu on the de-focused pane (via
	// menu.TransferFocus), refreshes the file overlay, and draws — all in one
	// call so no path can forget SyncFocus.
	prevFocus := a.tree.Focus
	a.focusToPane(p)
	button := args[0].Get("button").Int()
	if button == 2 {
		args[0].Call("preventDefault")
		a.onRightDown(p, r, sx, sy)
		return nil
	}
	if button == 1 {
		// Middle (third) button ascends the pane under the cursor — the
		// in-pane shortcut for the bar's crumb-click ascent (#222).
		// preventDefault suppresses the browser's middle-click autoscroll.
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

	// Left-drag on a pane boundary resizes the divider — same divider math
	// as the right-button resize, but it never closes a pane (clamped to a
	// recoverable minimum). Checked first so a grab near the edge wins over
	// content interactions. No divider on that side → falls through.
	// preventDefault, like the right-button path: the unprevented default
	// action (native selection/drag) engages past the OS drag threshold on
	// a FAST drag and steals the pointer from the canvas mid-resize
	// (issue #168; invisible to synthetic CDP input, so the e2e pins the
	// prevented flag rather than the steal itself).
	if a.armLeftResize(p, r, sx, sy) {
		args[0].Call("preventDefault")
		return nil
	}

	// Content descent (text/url/shell): every interactive surface is a DOM
	// overlay or native view that owns its own clicks (textarea, rendered
	// div, xterm, WebContentsView), and the bar slot owns the buttons
	// (issue #214). A canvas left-click reaching here is pane chrome or
	// margin — ascent lives on the middle button and the bar crumbs — so
	// it is swallowed whole.
	if p.ContentID() != "" {
		return nil
	}

	// The + button lives in the bar slot now (issue #214); barSlotClick
	// toggles the menu before a click ever reaches a pane.
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
			ratio := zoomtrans.EffectiveViewZoom(n.ViewZoom, zoomtrans.DefaultWellViewZoom)
			cp := dragdrop.ChildPreviewFor(ps, struct {
				X, Y, W, H     int64
				ViewCx, ViewCy float64
			}{X: n.X, Y: n.Y, W: n.W, H: n.H, ViewCx: n.ViewCx, ViewCy: n.ViewCy},
				ratio)
			cxF, cyF := cp.ChildCellAtScreen(sx, sy)
			tlX, tlY := cp.CellToScreen(float64(child.X), float64(child.Y))
			a.dragging.tileID = child.ID
			a.dragging.snapshotTile = *child
			a.dragging.cellOffsetX = cxF - float64(child.X)
			a.dragging.cellOffsetY = cyF - float64(child.Y)
			a.dragging.originScreenX = tlX
			a.dragging.originScreenY = tlY
			a.dragging.srcGridID = n.ChildGridID
			a.dragging.srcCellSize = cp.CellPx
			return nil
		}
		// Regular parent-grid drag of the well (or a non-well tile).
		a.dragging.tileID = n.ID
		cx, cy := ps.ScreenToCell(sx, sy)
		a.dragging.cellOffsetX = cx - float64(n.X)
		a.dragging.cellOffsetY = cy - float64(n.Y)
		a.dragging.snapshotTile = *n
		a.dragging.originScreenX, a.dragging.originScreenY = ps.CellToScreen(float64(n.X), float64(n.Y))
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
	// Promote to "started" once cursor has moved past the threshold.
	if !d.started {
		dxs := sx - d.startScreenX
		dys := sy - d.startScreenY
		if dxs*dxs+dys*dys >= dragThreshold*dragThreshold {
			d.started = true
			// On node drag, materialize the ghost; for moves, also hide
			// the original at its stored position so we don't see two
			// copies of the same stone. Template drags also need a
			// ghost so the synthetic tile follows the cursor.
			if d.tileID != "" || d.isTemplate {
				size := d.srcCellSize
				if size <= 0 {
					// Template drag: srcCellSize wasn't set by the
					// palette (it lives in screen px, not cells), so
					// use the focused pane's parent cell size.
					if src := a.tree.FindPane(d.originPaneID); src != nil {
						size = cellPx * src.Zoom
					} else {
						size = cellPx
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
				if d.tileID != "" {
					// Hide by ROW id — a clone is a different row that
					// looks the same, so a by-lineage hide would make
					// every clone vanish whenever its sibling is
					// picked up. (dragdrop.HiddenMatch + its test
					// cover the predicate.) Lives on the ghost: the
					// hide must outlive the drag (snap-back) and die
					// with the ghost.
					a.ghost.hiddenTileID = d.tileID
					a.ghost.hiddenPaneID = d.originPaneID
				}
			}
		} else {
			return nil
		}
	}
	if d.tileID == "" && !d.isTemplate {
		// Pan the source pane's parent-grid view smoothly. (A pan drag
		// only arms in grid mode — a content descent swallows the
		// mousedown — so there is no text-scroll arm here.)
		focused := a.tree.FindPane(d.originPaneID)
		if focused != nil {
			cellSize := cellPx * focused.Zoom
			focused.Cx -= (sx - d.curScreenX) / cellSize
			focused.Cy -= (sy - d.curScreenY) / cellSize
		}
	} else if a.ghost != nil {
		// Update the ghost from the SAME dragdrop.DecideDrop verdict the
		// left-drag commit (onMouseUp) uses, so a previewed action can never
		// differ from the committed one. clone=false → the move flavor.
		a.previewDrop(d, sx, sy, false)
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
	// Left-button release ends an in-flight pane-boundary resize: the ratio
	// was applied live during the move; the release decides collapse
	// (issue #203 — crush past the wall and let go to close the side).
	if a.leftResize != nil && args[0].Get("button").Int() == 0 {
		a.finishLeftResize()
		return nil
	}
	// URL descent: the live content box is handled by the native view;
	// swallow the matching mouseup over it so it doesn't leak into a
	// gridwell gesture.
	sx, sy := mouseXY(args[0], a.canvas)
	if p, r, ok := a.paneAtScreen(sx, sy); ok && a.isURLDescent(p) {
		if a.urlViewFor(p.ID) != nil && pointInLiveContent(r, sx, sy) && args[0].Get("button").Int() == 0 {
			return nil
		}
	}
	if a.dragging == nil {
		return nil
	}
	// A right-button clone drag commits only through the right-button
	// release path (finishRightDrag → commitRightClone), which clears
	// a.dragging before reaching here. Reaching the move-commit with a clone
	// still armed means a non-right button came up mid-drag (e.g. the user
	// pressed and released the left button while right-dragging). Ignore it
	// so the clone is never silently committed as a move — the gesture stays
	// armed and the eventual right-button release still clones.
	if a.dragging.clone {
		return nil
	}
	d := a.dragging
	a.dragging = nil
	// Reset any drag-time cursor change (e.g. "not-allowed" from
	// hovering a doc with the left button).
	a.canvas.Get("style").Set("cursor", "")
	sx, sy = mouseXY(args[0], a.canvas)

	// A plugin swatch clicked without dragging (no movement past the
	// threshold) enters that plugin: the + menu "click to descend" gesture.
	// A drag instead drops an exit-well link (commitTemplateDrop). The
	// descent is the SAME one a node-grid link tile takes — one verb, one
	// pushed frame — through a synthetic link tile placed at the pane's view
	// centre, so ascent lands back exactly here.
	if d.isTemplate && d.item.isPlugin && !d.started {
		if fp := a.tree.FindPane(d.originPaneID); fp != nil {
			well := paletteItemGhostNode(d.item)
			well.X, well.Y = int64(math.Floor(fp.Cx-0.5)), int64(math.Floor(fp.Cy-0.5))
			a.descend(fp, &well, nil)
		}
		return nil
	}

	// A url swatch clicked without dragging (no movement past the threshold) is
	// an EPHEMERAL visit: open the url modal and, on submit, descend into a live
	// url tile created in the off-grid scratch grid — visit a page without
	// placing a tile. A drag instead places a real url tile (commitTemplateDrop).
	if d.isTemplate && d.item.promotePane != "" && !d.started {
		return nil // a click on the current crumb: this is where you are
	}
	if d.isTemplate && d.item.primitive == tplURL && !d.started {
		// An ephemeral visit IS a live view — on a host without one (plain
		// browser) the modal would only produce a blank frozen tile, so say
		// why up front instead. Drag-create still places a real url tile.
		if !a.caps.LiveURL {
			a.menu.Close()
			a.reportErr(caps.GoLiveNotice())
			return nil
		}
		if fp := a.tree.FindPane(d.originPaneID); fp != nil && a.scratchGridForPane(fp) != "" {
			paneID := fp.ID
			a.menu.Close()
			a.openURLModal(a.urlSuggestCandidates(uuidOf(a.gridIDForPane(fp))),
				func(url string) {
					if vp := a.tree.FindPane(paneID); vp != nil {
						a.visitEphemeralURL(vp, url)
					}
				}, nil)
		}
		return nil
	}

	// A shell swatch clicked without dragging is an EPHEMERAL shell: created
	// in the off-grid scratch grid, descended into, PTY spawned. Ascent
	// DELETES it — tile row and tmux session with all its processes (gray
	// border says so). A drag instead places a real, persistent shell tile.
	if d.isTemplate && d.item.primitive == tplShell && !d.started {
		if fp := a.tree.FindPane(d.originPaneID); fp != nil && a.scratchGridForPane(fp) != "" {
			a.menu.Close()
			a.visitEphemeralShell(fp)
		}
		return nil
	}

	// Snapshot every world-read the drop decision needs, ONCE, using the
	// local d (a.dragging is already nil above). DecideDrop then picks the
	// action and the switch executes side effects. onMouseMove builds the
	// same DropInput for the ghost preview, so preview and commit cannot
	// diverge — that divergence was the trashcan-delete regression.
	in := dragdrop.DropInput{
		Started:       d.started,
		OriginFocused: d.originFocused,
		IsTemplate:    d.isTemplate,
		Clone:         d.clone, // always false here — clone commits via the right path above
		TileID:        d.tileID,
		OverDelete:    a.overDeleteButton(d, sx, sy),
	}
	t, haveT := a.dropTargetAt(sx, sy, d.tileID)
	in.HasTarget = haveT
	var dropX, dropY int64
	if haveT {
		in.Forbidden = a.dropForbiddenForMove(d, t)
		in.TargetReadOnly = a.gridKnownReadOnly(t.gridID)
		in.SameGrid = t.gridID == d.srcGridID
		in.CrossPlugin = dropCrossNamespace(d, t)
		dropX, dropY = t.cellAtCursor(sx, sy, d.cellOffsetX, d.cellOffsetY)
		in.SameCell = t.gridID == d.srcGridID && dropX == d.snapshotTile.X && dropY == d.snapshotTile.Y
		in.Occupied = a.occupiedForDrop(t.gridID, dropX, dropY,
			d.snapshotTile.W, d.snapshotTile.H, d.tileID)
	}

	switch dragdrop.DecideDrop(in) {
	case dragdrop.DropFocusOnly:
		// The click's only job was moving focus (done at mousedown).
		a.draw()
		return nil

	case dragdrop.DropNavigate:
		// Bare click (no movement) on an already-focused pane: navigation.
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
		if a.attemptDescentOrAscent(focused, r, sx, sy) {
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
		// Dropping the dragged tile on its source pane's corner button
		// (shown as a trashcan during a drag) deletes it — "drag it back to
		// the menu it came from". Resolved against the source pane's button,
		// not the grid under the cursor, so it works wherever that button sits.
		a.runDeleteTile(d, nil)
		a.ghost = nil
		a.draw()
		return nil

	case dragdrop.DropRejected:
		// Read-only doc, no target, forbidden cross-grid move, same cell, or
		// occupied — snap back without a doomed round-trip to the server.
		a.cancelDragSnapBack(d)
		return nil

	case dragdrop.DropLink:
		// Cross-namespace left-drag: the destination gains a LINK and the
		// source stays put — there is no cross-plugin move (owner decision
		// 2026-07-19). The ghost previewed this with the dashed chain badge.
		if a.ghost != nil {
			// The source was hidden for a would-be move; it stays — unhide it
			// now so the world reads "source intact + link appearing".
			a.ghost.hiddenTileID = ""
			a.ghost.hiddenPaneID = ""
		}
		a.landGhost(t.pane.ID, t.cellSize, t.originX+float64(dropX)*t.cellSize, t.originY+float64(dropY)*t.cellSize)
		a.commitLinkDrop(d, t, dropX, dropY)
		a.draw()
		return nil
	}

	// DropMove: animate ghost to the snapped cell in the target grid's coords.
	a.landGhost(t.pane.ID, t.cellSize, t.originX+float64(dropX)*t.cellSize, t.originY+float64(dropY)*t.cellSize)

	dstGridID := t.gridID
	srcGridID := d.srcGridID

	// Same-namespace left-drag is a move; clone is handled by the right-drag
	// path (commitRightClone in right_button.go) and never reaches here.
	// PlaceTile is the one placement writeback: id + the full (grid, x, y, w,
	// h) fact — no descent paths (2026-07-26 redesign; the
	// well-into-own-subtree refusal is the server's own ancestor walk now)
	// and no version claim (docs/simplify-plan.md S5: placement is layout,
	// last-writer-wins; the overlap check is what protects the grid).
	req := &rpc.PlaceTileRequest{
		TileID: d.tileID,
		GridID: dstGridID,
		X:      dropX,
		Y:      dropY,
		W:      d.snapshotTile.W,
		H:      d.snapshotTile.H,
	}
	// A drag carries no parked value: the ghost is presentation, and snapping
	// it back to its origin IS the honest reconcile the user can see.
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

// commitLinkDrop creates the LINK a cross-namespace left-drag drops: an exit
// well for a dragged well (same qualified child grid, framing, and label —
// identical to what a + menu plugin-swatch drop or a node-grid mount
// produces), a leaf link for text/url/shell/pane (link_target_id names the
// dragged tile — or, when the dragged tile is itself a leaf link, its
// TARGET, so links never chain through middleman tiles). The source tile
// is not touched.
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

// occupiedForDrop reports whether the dropped FOOTPRINT (x, y, w, h) in
// gridID overlaps any cached tile other than excludeID. A move passes the
// dragged tile's own id — mirroring the server's PlaceTile self-exclusion,
// so the preflight can never reject a placement the server would accept
// (#231: a large tile dragged a short distance crosses its own old
// footprint, which is not a collision). A clone passes "" — the source
// tile is a real neighbor there.
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

// startSnap animates the active ghost from its current position to (toX, toY)
// over the given duration. Replaces any prior animation.
// landGhost is the one drop landing: the ghost now belongs to paneID
// (drawn at that pane's cell size when cellSize > 0) and snaps to the
// screen cell (toX, toY). Four drop commits once repeated these lines.
func (a *App) landGhost(paneID string, cellSize, toX, toY float64) {
	if a.ghost != nil {
		a.ghost.paneID = paneID
		if cellSize > 0 {
			a.ghost.targetCellSize = cellSize
		}
	}
	a.startSnap(toX, toY, snapMs)
}

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
// in-flight drag d: the bar slot's trashcan (issue #214 — one global
// target, no longer the origin pane's corner).
//
// It takes the drag EXPLICITLY rather than reading a.dragging, because the
// commit path clears a.dragging before deciding what the drop means
// (onMouseUp / commitTileCenter both do `d := a.dragging; a.dragging = nil`).
// Reading the field here returned false at release — the tile fell through to a
// normal move and was placed under the trashcan instead of deleted — even
// though the live-drag preview looked correct.
func (a *App) overDeleteButton(d *dragState, sx, sy float64) bool {
	if d == nil || !d.started || d.tileID == "" {
		return false
	}
	return a.pointInPlus(sx, sy)
}

// attemptDescentOrAscent routes a bare left-click (no drag) at (sx, sy)
// inside pane p to the right navigation gesture. Left-click only ever
// descends now; ascent is the middle button or a right-click on the bar
// slot (see ascendPane).
//
//   - On a well: descend into the well.
//   - On a markdown file: descend into the file.
//   - Otherwise: no-op (selection is handled by the bare-click path
//     in onMouseUp before this is invoked).
//
// Returns true if a navigation gesture was performed (caller should skip
// further interpretation of the click).
func (a *App) attemptDescentOrAscent(p *pane.Pane, r pane.Rect, sx, sy float64) bool {
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
	// An address-less url tile (dropped bare — issue #209): the first
	// descent is where the address gets asked for. A url LINK resolves its
	// address through the target and never prompts.
	if hit.Kind == rpc.KindURL && hit.URLString == "" && hit.LinkTargetID == "" {
		a.openConfigureURL(p, hit)
		return true
	}
	if !rpc.IsWellKind(hit.Kind) && !rpc.IsContentDescentKind(hit.Kind) &&
		!rpc.IsWorkspaceKind(hit.Kind) {
		return false
	}
	// ONE descent: which kind of frame it pushes is the TILE's declaration
	// (nav.go), not this call site's.
	a.descend(p, hit, nil)
	return true
}

// totalTransitionMs is the total wall-clock duration of a descent or
// ascent transition. The same value is used for both so the UX is
// symmetric in feel. A var (not const) solely for the e2e-only
// setTransitionMs testhook, which stretches it so a spec can inject an
// SSE event DURING a transition deterministically (I11); production has
// no writer.
var totalTransitionMs = 350.0

// zoomDistFactor scales log-zoom distance to a "perceived px" unit so we
// can apportion animation time between pan and zoom phases. Tuned so a
// zoom by factor e ≈ 256 px-equivalent — about four cells. Bigger than
// pan distances tend to be in practice, so zoom phases get the bulk of
// the time, which matches the user's intent that the zoom is "the
// action" and the pan is "the setup".
const zoomDistFactor = 4.0

// Descents through a link tile (a namespace crossing) live in descend
// (nav.go): a mounted well and a cross-plugin clone descend through the
// SAME path a normal well does, with a frame pushed so ascent returns
// exactly here.

// descentTextMode applies textedit.DescentMode (the one owner) to a
// cached tile row at descent; cursorURL is the restore path's extra
// input (an address that encodes a text cursor).
func (a *App) descentTextMode(file *rpc.Tile, cursorURL bool) string {
	return textedit.DescentMode(textedit.ModeInput{
		Kind: file.Kind, ServesPage: file.ServesPage, ReadOnly: a.tileReadOnly(file),
		Cached: true, CursorURL: cursorURL, Stored: file.TextMode,
	})
}

// persistedGridView resolves the framing the grid at (anchor, path) was
// LEFT at, from the row that owns it: the containing well's view_* for a
// nested grid, the plugin's persisted root view for a root. The restore
// for every ascent with no session state (a reload mid-descent) and the
// boot viewport — before this, those paths landed on 0,0,zoom-1, a
// framing the user never set (the guiding rule, violated on the way
// out). ok=false when nothing is persisted or the owning row isn't
// cached; callers keep their legacy fallback then.
func (a *App) persistedGridView(p *pane.Pane, anchor string, path []string) (cx, cy, zoom float64, ok bool) {
	r := paneRectFor(a, p)
	if r.W <= 0 || r.H <= 0 {
		return 0, 0, 0, false
	}
	if len(path) == 0 {
		// A root grid: the read side of persistFraming's root arm — the
		// same 1×1 synthetic doorway, inverted; a root's framing rides
		// its PluginInfo.
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
	w := zoomtrans.Well{X: t.X, Y: t.Y, W: t.W, H: t.H,
		ViewCx: t.ViewCx, ViewCy: t.ViewCy, ViewZoom: t.ViewZoom}
	cx, cy, zoom = zoomtrans.StoredView(w, r.W, r.H, cellPx)
	return cx, cy, zoom, true
}

// autoLiveOnRestore is autoLiveOnDescent for the RESTORE paths — reload
// (applyURLState), workspace install, an ascent landing back on a content
// frame that was stacked under a deeper visit (landOnFrame). The tile row is not necessarily cached at
// restore time, so it is fetched first; the pane is re-resolved when the
// read lands (the user may have moved on — never override where they went).
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
// (anchor, path) no longer leads to the descended tile's grid (issue
// #234): the tile moved — its id is immutable, its path is not. A scoped
// `id:` Search (the one find verb, issue #244) answers with the CURRENT
// containing-well chain; the pane re-anchors at the owning root with the
// fresh path, so the descent binds and the crumbs show a true path from
// root. The workspace persister derives the corrected layout from the
// live tree on its next tick, so the heal persists with no dedicated
// writer. Runs on the restore goroutine (a blocking read is fine there);
// an unsearchable tile (a plugin without Search) leaves today's
// frozen-preview state.
func (a *App) healStalePanePath(paneID string, tile *rpc.Tile) {
	fp := a.tree.FindPane(paneID)
	if !pane.StillDescended(fp, tile.ID) {
		return
	}
	if a.isEphemeralTile(fp, tile) {
		// An EPHEMERAL descent is deliberately focused OFF the pane's grid
		// (the scratch tile rides above whatever place the pane frames):
		// the path is not stale, the tile is elsewhere BY DESIGN — healing
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
// just-descended tile: open the url view, attach/create the shell PTY, probe
// an unknown shell session first, or stay frozen (text, browser hosts, dead
// sessions). The one auto-live owner — the refresh affordances remain as the
// RETRY for the cases this stays frozen on. tile is the descent-time row,
// passed by value: an ephemeral (scratch) tile is in no cached grid.
func (a *App) autoLiveOnDescent(paneID string, tile *rpc.Tile) {
	tileID := tile.ID
	fp := a.tree.FindPane(paneID)
	if !pane.StillDescended(fp, tileID) {
		return
	}
	// The shell facts key by the CONTENT id (a link attaches its target's
	// session) — the same reads shellRefreshButtonVisible does, so the two
	// decisions can never disagree about a dead session.
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
			// Re-check the pane is still in THIS descent when the verdict
			// lands — the probe is async and the user may have moved on.
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
	// SetTextView (and the framed-window cache patch) are text-tile
	// concerns — URL and shell tiles don't carry text_x/text_y/text_w
	// /text_h, and the server's SetTextView rejects non-text kinds with
	// InvalidArgument. Routing them through would surface as a 400 the
	// user has to read. A serves_page descent is web content — no text
	// framing either.
	if file.Kind != rpc.KindText || file.ServesPage {
		return
	}
	gid := a.gridIDForPane(p)
	r := paneRectFor(a, p)
	scrollX := int64(p.TextScrollX + 0.5)
	scrollY := int64(p.TextScrollY + 0.5)

	// Content: read the tile's OWN content-store entry, and only when it
	// carries an unsaved edit. Never the DOM. The old code read the singleton
	// textarea here and attributed it to whatever tile THIS pane pointed at —
	// which is how a bulk flush (pane collapse, workspace boundary) over a
	// pane the singleton wasn't bound to saved one document's bytes as
	// another's content (the 2026-07-18 cross-tile stomp). It also posted
	// unconditionally, so a merely-opened tile rewrote its blob and bumped
	// its version on every visit; dirty-gating makes a pure read write-free
	// (the guiding rule: reading never mutates).
	// Read-only host tiles have no write-back at all: no content (the body
	// is reconciler output) and no framing store either — the fs plugin's
	// SetTile refuses text framing, so posting SetTextView from here only
	// manufactured an error strip (#236). The mode/scroll stay session
	// facts for them.
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
	// Through the per-tile save queue (issue #140): a debounced keystroke
	// save may still be in flight, and this flush claims a version too — the
	// queue serializes them and the version is read at send time.
	a.textSaves.Enqueue(file.ID, func() {
		curVersion := file.Version
		if g, ok := a.c.Grid(gid); ok {
			if f, ok := g.Tiles[file.ID]; ok {
				curVersion = f.Version
			}
		}
		// Update content first if the user was editing. The CONTENT write
		// claims the save basis — the version of the bytes the entry derives
		// from — never the row version read above: a foreign writer's event
		// advances the row without this client seeing the new bytes, and
		// claiming it would save the stale entry right over the foreign edit
		// (the remote-stomp bug). A stale basis conflicts at the server and
		// reconciles visibly instead.
		if hasBuf {
			// The write addresses the CONTENT owner (a link doc saves under
			// its target's id — flushTileContent's discipline); the row
			// version is a valid fallback only when this row IS the owner.
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
				// The SetTextView below claims THIS row's version; only
				// advance it when the content write bumped this same row
				// (a link's target version is a different row's fact).
				curVersion = tile.Version
			}
		}
		// Persist the framed window + mode so re-descent and the preview
		// honor "however you left it" across reloads — only when something
		// changed (textedit.FramingChanged, the one rule): a pure
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
		// The menu belongs to the pane's NODE (remote-menu): the drop
		// rules compare this against the destination's node.
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
	switch item.primitive {
	case tplWell:
		return rpc.Tile{Kind: rpc.KindWell, W: 1, H: 1}
	case tplMarkdown:
		return rpc.Tile{Kind: rpc.KindText, W: 1, H: 1}
	case tplURL:
		return rpc.Tile{Kind: rpc.KindURL, W: 1, H: 1}
	case tplShell:
		return rpc.Tile{Kind: rpc.KindShell, W: 1, H: 1, AltText: "shell"}
	case tplPane:
		return rpc.Tile{Kind: rpc.KindPane, W: 1, H: 1, AltText: "workspace"}
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

	// A plugin item drops into the destination grid (drag-a-plugin-onto-a-
	// grid): an exit-well LINK to its root grid — a connection row drops
	// the same way, its chained root already qualified (v2 #269; links
	// are the standing cross-boundary vocabulary, 2026-07-19). Only
	// writable grids accept it; anything else snaps back.
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

	// A primitive belongs to the node whose menu offered it (remote-menu,
	// 2026-08-16): the swatch was gated by THAT node's grids and policy,
	// and creating "a remote node's text tile" inside a local grid is a
	// category error. Same-node drops only; a cross-node drop refuses
	// VISIBLY (charter §6) and snaps back.
	if a.paneNodeNS(destPane) != d.menuNS {
		a.reportErr(errsurface.Info, "menu",
			"this menu belongs to another node — drop into a grid on that node, or open the menu here")
		a.cancelDragSnapBack(d)
		return
	}

	// EVERY template commits immediately with the snap-and-create gesture
	// (issue #209: drop first — the drop never prompts; whatever a kind
	// needs to be useful is asked for on the first DESCENT, so create is
	// one experience everywhere: drop, descend, fill in).
	targetX, targetY := dpscreen.CellToScreen(float64(dropX), float64(dropY))
	a.landGhost(destPane.ID, 0, targetX, targetY)

	switch d.item.primitive {
	case tplWell:
		a.createWellAtCell(destPane, dropX, dropY)
	case tplMarkdown:
		a.createTextAtCell(destPane, []byte{}, dropX, dropY)
	case tplURL:
		if d.item.promotePane != "" {
			a.promoteEphemeralURL(d.item.promotePane, destPane, dropX, dropY)
			break
		}
		a.createURLAtCell(destPane, dropX, dropY)
	case tplShell:
		a.createShellAtCell(destPane, dropX, dropY)
	case tplPane:
		a.createPaneAtCell(destPane, dropX, dropY)
	}
	a.menu.Close()
}

// createPluginLinkAtCell fires CreateWell with the plugin's qualified root
// grid as the child — an exit-well LINK, the same tile a cross-plugin clone
// of the plugin's node-grid tile creates (the Mount RPC is gone; CreateTile
// is the one create). The link's framing seeds from the plugin's persisted
// root view so its preview shows what descent will show.
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

// createWellAtCell fires CreateWell at the given cell. Footprint is 1×1.
// Wells are created UNNAMED (naming happens from inside, via the name
// bubble — issue #118).
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

// createURLAtCell fires CreateURL at the given cell — ADDRESS-LESS (issue
// #209: drop first). The tile lands inert; the first descent prompts for
// the address (openConfigureURL) and writes it as the tile's content.
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
// descent (issue #209: drop first, prompt on descent), reusing the url
// modal with its visited-url suggestions. Submit writes the address as
// the tile's content (the store's url arm: versioned, validated, bumps) and
// then descends, so the fill-in flows straight into the page. EVERY descent
// goes live (issue #202), so no special go-live handling.
func (a *App) openConfigureURL(p *pane.Pane, t *rpc.Tile) {
	gid := a.gridIDForPane(p)
	paneID, id := p.ID, t.ID
	// The address IS content, so this write claims a version like every
	// content write (docs/simplify-plan.md S5) — the row as the descent saw
	// it, which is exactly the value the user is filling in.
	version := t.Version
	candidates := a.urlSuggestCandidates(uuidOf(gid))
	a.openURLModal(candidates, func(url string) {
		go func() {
			// Through the plain dispatcher, not postWriteContent: the typed
			// url has no cache entry backing it (the modal is the only
			// holder), so the content path's "the dirty entry is the record"
			// rule can't cover it — the dispatcher parks the closure itself
			// on a transport failure (audit #10, 2026-08-14) and the address
			// lands on the retry kick; only the descend is skipped.
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

// scratchGridForPane returns the qualified scratch grid id of the plugin the
// pane is currently in — where ephemeral url visits land — or "" if that plugin
// has none (fs/proc don't support ephemeral visits).
func (a *App) scratchGridForPane(p *pane.Pane) string {
	gid := a.gridIDForPane(p)
	// The fact rides ON the grid (Grid.ScratchGridID, stamped by the serving
	// node and chained through mounts) — a local plugin-list lookup cannot
	// answer for a remote plugin behind a transit mount.
	if g, ok := a.c.Grid(gid); ok && g.Meta.ScratchGridID != "" {
		return g.Meta.ScratchGridID
	}
	// Fallback for an uncached grid: the local plugin list (local plugins
	// only — first segment).
	want := uuidOf(gid)
	for _, pl := range a.plugins {
		if pl.UUID == want {
			return pl.ScratchGridID
		}
	}
	return ""
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
	scratch := a.scratchGridForPane(p)
	if scratch == "" {
		return // plugin has no scratch grid — nothing to visit into
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

// isEphemeralTile reports whether t is an ephemeral (scratch-grid) tile of
// the plugin pane p sits in. The one client-side derivation of "ephemeral":
// the tile's grid IS the plugin's scratch grid — both facts are server-owned
// (Grid.ScratchGridID rides on the grid, chained through mounts), so no new
// wire field is needed. Ephemeral tiles are deleted on ascent; the descended
// pane border goes gray to say so (issue #85).
func (a *App) isEphemeralTile(p *pane.Pane, t *rpc.Tile) bool {
	s := a.scratchGridForPane(p)
	return s != "" && t.GridID == s
}

// leavingEphemeral is THE decision that a pane leaving tile t deletes it:
// the tile is ephemeral AND no other pane still shows it (pane.OtherPaneShows
// — a split clones the visit; issue #111). Every ascent-shaped path (the
// animated ascent, the instant pop, promotion onto a grid) asks here, so no
// path can forget the guard.
func (a *App) leavingEphemeral(p *pane.Pane, t *rpc.Tile) bool {
	return a.isEphemeralTile(p, t) && !a.tree.OtherPaneShows(p.ID, t.ID)
}

// deleteEphemeralTile removes an ascended-from ephemeral tile — gray means
// gone: the row is deleted, and for a shell the plugin kills its tmux session
// (all processes) as part of the delete. A failure surfaces on the strip
// (charter §6); otherwise the tile would silently leak in the scratch grid
// until the startup sweep.
func (a *App) deleteEphemeralTile(tileID string) {
	go func() {
		// No claim: a delete is the USER's explicit action, and the stream
		// close that precedes it triggers the plugin's detach-time title
		// capture — which used to bump the row and refuse this delete,
		// forcing a one-shot re-claim here. Captures no longer bump and a
		// delete no longer claims (docs/simplify-plan.md S5), so the whole
		// race is gone rather than absorbed.
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
// scratch grid and descends into it, spawning the PTY — "open a shell" from
// the menu's shell swatch (clicked, not dragged). The shell twin of
// visitEphemeralURL, with the opposite exit contract: ascent DELETES the tile
// and its tmux session (issue #85) — gray border warns not to leave
// persistent work there.
func (a *App) visitEphemeralShell(p *pane.Pane) {
	scratch := a.scratchGridForPane(p)
	if scratch == "" {
		return // plugin has no scratch grid — nothing to open into
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

// openLinkBelow handles a link opened OUT of a live tile — a new-window
// intent from a live url view (target=_blank, window.open, ctrl/cmd-click —
// issue #111) and a url activated in a live shell (issue #207): split the
// pane horizontally and open the url as an EPHEMERAL visit in the new lower
// half. The link renders next to the page/terminal it came from, on the
// same plugin session, and dies on ascent like every ephemeral visit (#85).
// If the split fails (degenerate pane) the visit opens in place instead —
// the link must never be silently dropped.
func (a *App) openLinkBelow(paneID, url string) {
	p := a.tree.FindPane(paneID)
	if p == nil {
		return
	}
	// SplitOnSideAt splits the FOCUSED pane; the link's pane may not be it
	// (a background page can window.open). Focus it first — that is also
	// where the user's attention is about to go.
	a.focusToPane(p)
	// The universal pane minimum applies to programmatic splits too (issue
	// #167): a pane too short for two minimum panes opens the visit in
	// place instead of birthing a sub-minimum pane (SplitOnSideAt itself
	// only clamps the ratio — its doc makes sub-min rejection the caller's
	// job).
	if !pane.CanSplit(pane.SideBottom, paneRectFor(a, p)) {
		a.visitEphemeralURL(p, url)
		return
	}
	newP, err := a.tree.SplitOnSideAt(pane.SideBottom, 0.5)
	if err != nil {
		a.visitEphemeralURL(p, url)
		return
	}
	// The clone inherits the source's content frame, which a
	// live view can't duplicate — same rule as commitSplit: ascend the file
	// level so the visit descends from the containing grid. (The ephemeral
	// delete-on-ascent is guarded by leavingEphemeral, so this ascent
	// never deletes the tile the SOURCE pane still shows.)
	if newP.ContentID() != "" {
		a.ascend(newP, 1, true)
	}
	a.draw()
	a.scheduleURLUpdate()
	a.visitEphemeralURL(newP, url)
}

// shellURLActivate handles a click on an http(s) url in a live shell (the xterm
// link provider's activate): open it below, exactly like a link a live url
// view pops (issue #207 — one behavior for links out of live tiles; the old
// in-place descent stashed the shell on a session-only side stack, which any
// place-restore dropped — the issue #208 double-ascend; since S8 a stacked
// visit is just another frame). A no-op if the
// shell is no longer the pane's active descent.
func (a *App) shellURLActivate(paneID, url string) {
	if p := a.tree.FindPane(paneID); p != nil && p.ContentID() != "" {
		a.openLinkBelow(paneID, url)
	}
}

// createShellAtCell fires CreateShell at the given cell, then
// auto-descends and auto-spawns the PTY — the user dropped a shell
// to use a shell, not to look at a placeholder. The first refresh
// creates the tile's gridwell-private tmux session; subsequent
// ascent / re-descent shows the frozen JPEG and refresh reattaches
// to the same tmux session (state preserved).
func (a *App) createShellAtCell(p *pane.Pane, cellX, cellY int64) {
	gid := a.gridIDForPane(p)
	req := &rpc.CreateShellRequest{
		GridID: gid, X: cellX, Y: cellY, W: 1, H: 1,
	}
	// The drop just lands the tile — no auto-descend, like every other
	// primitive since #209 (issue #241; the descend-on-create here was a
	// leftover of the pre-#209 url flow). The FIRST descent creates the
	// session: DecideAutoLive's fresh-shell arm (no preview blob).
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
// grid — the bar crumb dragged onto a grid (2026-08-27). The tile is
// created with the visit's CURRENT address (the page may have navigated);
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
// freeze onto the NEW tile (never the row about to die); the ephemeral
// row is deleted (gray means gone) unless a split sibling still shows
// it; the pane relocates to the new tile's
// grid (pane.RelocateTo — the nav chain and the next ascent read the new
// place, its ascent viewport being the destination pane's); and the page
// goes live again on the new tile.
func (a *App) finishPromote(originPaneID, destPaneID, oldID string, created rpc.Tile) {
	op := a.tree.FindPane(originPaneID)
	dp := a.tree.FindPane(destPaneID)
	if !pane.StillDescended(op, oldID) || dp == nil {
		return // moved on mid-flight: the tile stays where it was dropped
	}
	a.closeURLStreamTo(op.ID, &freezeTarget{tileID: created.ID, gridID: created.GridID}, true)
	// The row dies only if no sibling pane still shows the visit (a split
	// clone keeps it, and deletes it on ITS ascent) — the same guard every
	// ascent applies, through the same door.
	if old := a.cachedTileByID(oldID); old != nil && a.leavingEphemeral(op, old) {
		a.deleteEphemeralTile(oldID)
	}
	// The pane follows its content: RelocateTo replaces the visit's frame
	// with one on the destination's stack, so the next ascent lands where
	// the tile now lives (one owner — there is no separate saved viewport
	// to keep in step).
	op.RelocateTo(dp, created.ID)
	a.placeURLView(op.ID, created)
	a.refreshFileOverlay()
	a.scheduleURLUpdate()
	a.draw()
}
