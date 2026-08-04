//go:build js && wasm

package main

import (
	"context"
	"math"
	"slices"
	"syscall/js"

	"github.com/josephburnett/gridwell/client/anim"
	"github.com/josephburnett/gridwell/client/caps"
	"github.com/josephburnett/gridwell/client/dragdrop"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/gesture"
	"github.com/josephburnett/gridwell/client/gridpath"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/pluginhealth"
	"github.com/josephburnett/gridwell/client/shellconn"
	"github.com/josephburnett/gridwell/client/wsbar"
	"github.com/josephburnett/gridwell/client/zoomtrans"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// Animation durations in milliseconds. Tuned for "stone settling" feel.
const (
	snapMs     = 110.0
	snapBackMs = 220.0
)

// installCanvasInput attaches mouse listeners to the canvas. Gridwell
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
	a.canvas.Call("addEventListener", "mousemove", js.FuncOf(a.onMouseMove))
	a.canvas.Call("addEventListener", "mouseup", js.FuncOf(a.onMouseUp))
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

// paneAtScreen returns the pane (and its rect) under the given screen coords,
// or (nil, pane.Rect{}, false) if no pane covers the point.
func (a *App) paneAtScreen(sx, sy float64) (*pane.Pane, pane.Rect, bool) {
	rects := a.layoutPanes()
	for id, r := range rects {
		if sx >= r.X && sy >= r.Y && sx < r.X+r.W && sy < r.Y+r.H {
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
	// Wheel over the BAR band zooms the current pane as if the cursor were
	// at the pane's center (issue #220): the escape hatch for a grid tiled
	// wall-to-wall with wells, where every content position claims the
	// well-zoom (#210) and no empty spot remains.
	if bx, top, bw, barOK := a.bottomBarRect(); barOK &&
		sy >= top && sy < top+wsbar.RowH && sx >= bx && sx < bx+bw {
		if p := a.tree.FocusedPane(); p != nil && p.TextFocus == "" {
			r := a.paneRectByID(p.ID)
			a.wheelZoomPaneAt(p, r, dy, r.X+r.W/2, r.Y+r.H/2)
		}
		return nil
	}
	p, r, ok := a.paneAtScreen(sx, sy)
	if !ok {
		return nil
	}
	// Routing is the pure gesture.ClassifyWheel: this handler only resolves
	// the impure facts (live view attached? cursor in the content box? an
	// enterable well under the cursor?) and executes the verdict.
	var hoverWell *rpc.Tile
	if p.TextFocus == "" {
		if t := a.tileAtScreen(p, r, sx, sy); t != nil && rpc.IsWellKind(t.Kind) && t.ChildGridID != "" {
			hoverWell = t
		}
	}
	switch gesture.ClassifyWheel(gesture.WheelInput{
		TextFocused:       p.TextFocus != "",
		URLDescent:        a.isURLDescent(p),
		LiveURLView:       a.urlViewFor(p.ID) != nil,
		InContentBox:      pointInPaneContent(r, sx, sy),
		TextModeRendered:  p.TextMode == rpc.TextModeRendered,
		OverEnterableWell: hoverWell != nil,
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
		// the settle persister posts one SetWellView per tile at flush.
		ps := paneToDragdrop(p, r)
		x0, y0 := ps.CellToScreen(float64(hoverWell.X), float64(hoverWell.Y))
		x1, _ := ps.CellToScreen(float64(hoverWell.X)+1, float64(hoverWell.Y))
		parentCell := x1 - x0
		wpx := float64(hoverWell.W) * parentCell
		hpx := float64(hoverWell.H) * parentCell
		zw := zoomtrans.Well{
			X: hoverWell.X, Y: hoverWell.Y, W: hoverWell.W, H: hoverWell.H,
			ViewX: hoverWell.ViewX, ViewY: hoverWell.ViewY, ViewZoom: hoverWell.ViewZoom,
		}
		// The float center accumulates across the burst (issue #219): first
		// notch seeds from the stored origin; later notches feed the drift
		// back in, so cursor-anchored zoom actually travels.
		cx0 := float64(hoverWell.ViewX) + float64(hoverWell.W)/2
		cy0 := float64(hoverWell.ViewY) + float64(hoverWell.H)/2
		if st, ok := a.wellWheelPending[hoverWell.ID]; ok {
			cx0, cy0 = st.cx, st.cy
		}
		cx1, cy1, ratio, changed := zoomtrans.WellWheelView(dy, zw, parentCell,
			sx-(x0+wpx/2), sy-(y0+hpx/2), cx0, cy0, zoomFactor, wellZoomRatioMin, wellZoomRatioMax)
		if !changed {
			return nil
		}
		updated := *hoverWell
		updated.ViewX = zoomtrans.ViewOriginFromCenter(cx1, hoverWell.W)
		updated.ViewY = zoomtrans.ViewOriginFromCenter(cy1, hoverWell.H)
		updated.ViewZoom = ratio
		a.c.Apply(rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: updated}})
		a.wellWheelPending[hoverWell.ID] = wellWheelDrift{gridID: a.gridIDForPane(p), cx: cx1, cy: cy1}
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
	if p.TextFocus != "" {
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
				X, Y, W, H, ViewX, ViewY int64
			}{X: n.X, Y: n.Y, W: n.W, H: n.H, ViewX: n.ViewX, ViewY: n.ViewY},
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
					// Hide by ROW id, not ObjectID — clones share an
					// ObjectID with the source, so a by-lineage hide
					// makes every clone vanish whenever its sibling
					// is picked up. (dragdrop.HiddenMatch + its test
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
		if a.urlViewFor(p.ID) != nil && pointInPaneContent(r, sx, sy) && args[0].Get("button").Int() == 0 {
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
	// descent is the SAME portal path a node-grid link tile takes —
	// startDescent pushes the return frame and swaps the anchor — through a
	// synthetic link tile placed at the pane's view centre, so ascent lands
	// back exactly here.
	if d.isTemplate && d.item.isPlugin && !d.started {
		if fp := a.tree.FindPane(d.originPaneID); fp != nil {
			// Portal is a place change: flush framing still inside the
			// settle window first (issue #190).
			a.flushFramingSave()
			well := paletteItemGhostNode(d.item)
			well.X, well.Y = int64(math.Floor(fp.Cx-0.5)), int64(math.Floor(fp.Cy-0.5))
			a.startDescent(fp, &well)
		}
		return nil
	}

	// A url swatch clicked without dragging (no movement past the threshold) is
	// an EPHEMERAL visit: open the url modal and, on submit, descend into a live
	// url tile created in the off-grid scratch grid — visit a page without
	// placing a tile. A drag instead places a real url tile (commitTemplateDrop).
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
		targetX := t.originX + float64(dropX)*t.cellSize
		targetY := t.originY + float64(dropY)*t.cellSize
		if a.ghost != nil {
			a.ghost.paneID = t.pane.ID
			a.ghost.targetCellSize = t.cellSize
			// The source was hidden for a would-be move; it stays — unhide it
			// now so the world reads "source intact + link appearing".
			a.ghost.hiddenTileID = ""
			a.ghost.hiddenPaneID = ""
		}
		a.startSnap(targetX, targetY, snapMs)
		a.commitLinkDrop(d, t, dropX, dropY)
		a.draw()
		return nil
	}

	// DropMove: animate ghost to the snapped cell in the target grid's coords.
	targetX := t.originX + float64(dropX)*t.cellSize
	targetY := t.originY + float64(dropY)*t.cellSize
	if a.ghost != nil {
		a.ghost.paneID = t.pane.ID
		a.ghost.targetCellSize = t.cellSize
	}
	a.startSnap(targetX, targetY, snapMs)

	dstGridID := t.gridID
	srcGridID := d.srcGridID

	// Same-namespace left-drag is a move; clone is handled by the right-drag
	// path (commitRightClone in right_button.go) and never reaches here.
	// PlaceTile is the one placement writeback: id + version claim + the full
	// (grid, x, y, w, h) fact — no descent paths (2026-07-26 redesign; the
	// well-into-own-subtree refusal is the server's own ancestor walk now).
	req := &rpc.PlaceTileRequest{
		TileID:  d.tileID,
		Version: d.snapshotTile.Version,
		GridID:  dstGridID,
		X:       dropX,
		Y:       dropY,
		W:       d.snapshotTile.W,
		H:       d.snapshotTile.H,
	}
	a.postCrossGridMutate("PlaceTile", srcGridID, dstGridID, func(ctx context.Context) (*rpc.Tile, error) {
		return a.cl.PlaceTile(ctx, req)
	}, d)
	a.draw()
	return nil
}

// commitLinkDrop creates the LINK a cross-namespace left-drag drops: an exit
// well for a dragged well (same qualified child grid, framing, and label —
// identical to what a + menu plugin-swatch drop or a node-grid mount
// produces), a leaf link for text/url/shell/pane (link_target_id names the
// dragged tile — or, when the dragged tile is itself a leaf link, its
// TARGET, so links never chain through middleman tiles). Provenance
// object_id rides along; the source tile is not touched.
func (a *App) commitLinkDrop(d *dragState, t *dropTarget, dropX, dropY int64) {
	src := d.snapshotTile
	dstGridID := t.gridID
	if rpc.IsWellKind(src.Kind) {
		req := &rpc.CreateWellRequest{
			GridID: dstGridID, X: dropX, Y: dropY, W: src.W, H: src.H,
			ChildGridID: src.ChildGridID, Label: src.AltText,
			ViewX: src.ViewX, ViewY: src.ViewY, ViewZoom: src.ViewZoom,
			ObjectID: src.ObjectID,
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
		ObjectID: src.ObjectID,
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
	if p.TextFocus != "" {
		// Inside a text/url/shell descent, a bare click isn't navigation:
		// ascent lives on the middle button and the bar's crumb click.
		return false
	}
	cellX, cellY := cellAtScreen(p, r, sx, sy)
	hit := a.tileAtCell(p, cellX, cellY)
	if hit == nil {
		return false
	}
	// The pane is about to change place: flush framing still inside the
	// settle window (issue #190), while the viewport still belongs to the
	// place it describes.
	a.flushFramingSave()
	switch {
	case rpc.IsWellKind(hit.Kind):
		a.startDescent(p, hit)
		return true
	case rpc.IsContentDescentKind(hit.Kind):
		// An address-less url tile (dropped bare — issue #209): the first
		// descent is where the address gets asked for. A url LINK resolves
		// its address through the target and never prompts.
		if hit.Kind == rpc.KindURL && hit.URLString == "" && hit.LinkTargetID == "" {
			a.openConfigureURL(p, hit)
			return true
		}
		a.startTextDescent(p, hit, nil)
		return true
	case rpc.IsWorkspaceKind(hit.Kind):
		// The third descent verb: swap the whole pane tree for the stored
		// workspace. The way back out is the bar.
		a.startWorkspaceDescent(p, hit)
		return true
	}
	return false
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

// panDist is the wasm-side adapter for zoomtrans.PanDist, binding the
// renderer's base cell size.
func panDist(dx, dy, zoom float64) float64 {
	return zoomtrans.PanDist(dx, dy, zoom, cellPx)
}

// zoomDist is the wasm-side adapter for zoomtrans.ZoomDist, binding
// the renderer's base cell size and the zoom-vs-pan weighting factor.
func zoomDist(z1, z2 float64) float64 {
	return zoomtrans.ZoomDist(z1, z2, cellPx, zoomDistFactor)
}

// ascendPane performs the appropriate ascent for pane p: a file ascent
// when it's descended into a text/url/shell tile, a well ascent when it's
// in a child grid, a portal ascent (pop the + menu entry stack) when it
// entered a plugin at its root, nothing at the launcher. This is the single
// entry point for every ascent gesture (the middle button, the bar's
// crumb click).
func (a *App) ascendPane(p *pane.Pane) {
	switch {
	case p.TextFocus != "":
		a.startTextAscent(p)
	case len(p.Path) > 0:
		a.startAscent(p)
	case len(p.Up) > 0:
		a.ascendPortal(p)
	}
}

// Portal descents (anchor swaps through a link tile) live in startDescent:
// a plugin tile on the node grid, a mounted well, and a cross-plugin clone
// all descend through the SAME path a normal well does, with a frame pushed
// so ascent returns exactly here. There is no separate enterPlugin anymore —
// the launcher is a real grid.

// ascendPortal returns the pane to wherever it jumped into the current plugin
// from (another plugin, or the launcher), reopening the + menu if it was open
// when the user entered.
//
// Returning to the launcher animates a symmetric zoom-out — the exact inverse
// of enterPlugin's zoom-in, landing back on the plugin's launcher tile (now a
// live grid preview). Returning to another plugin's grid via an in-grid + menu
// portal stays an instant cut: that entry footprint isn't reconstructed here.
func (a *App) ascendPortal(p *pane.Pane) {
	f, ok := p.TopFrame()
	if !ok {
		return
	}
	// The portal's containing tile: the link well in the frame's leaf grid
	// whose child is the pane's current anchor. When it resolves, the ascent
	// writes the pane's framing back onto it (the SAME face-#3 writeback a
	// normal well ascent does — for a node-grid tile the provider maps it
	// onto the plugin's SetRootView) and animates onto its footprint.
	well := a.portalWellForFrame(p, f)
	if well != nil {
		framePath := slices.Clone(f.Path)
		a.persistWellView(p, well, f.Anchor, framePath)
		a.animatePortalAscent(p, f, well)
		return
	}
	// No containing link tile — a + menu portal (the origin grid holds no
	// tile for it). The framing writeback still happens, just without a tile
	// to carry it: write the plugin's root view directly (the SAME fact a
	// node-grid tile write routes onto via SetRootView), so re-entering the
	// plugin from the menu lands at the left-off view.
	a.persistPluginRootView(p)
	if !p.PopFrame() {
		return
	}
	if f.MenuOpen {
		a.menu.Open(p.ID)
	}
	a.fetchGrid(a.gridIDForPane(p))
	a.draw()
	a.scheduleURLUpdate()
}

// persistPluginRootView persists the pane's viewport as its plugin's root
// view when the pane sits at a plugin ROOT grid — the tile-less half of the
// portal framing writeback, fired at portal ascent and by the settle
// persister (flushFramingSave). Same intrinsic math as persistWellView over
// the 1×1 synthetic plugin tile (rpc.PluginWellTile) the pane descended
// through, and the same no-op guard so quiet calls don't churn the store.
// The local PluginInfo copy of the root view (a cache of the Info
// handshake) reconciles immediately so the next + menu descent frames to
// what was just saved.
func (a *App) persistPluginRootView(p *pane.Pane) {
	if len(p.Path) > 0 || p.TextFocus != "" {
		return
	}
	pl, ok := a.pluginByUUID(uuidOf(p.Anchor))
	if !ok || pl.RootGridID != p.Anchor {
		return
	}
	newViewX := zoomtrans.ViewOriginFromCenter(p.Cx, 1)
	newViewY := zoomtrans.ViewOriginFromCenter(p.Cy, 1)
	r := paneRectFor(a, p)
	overtake := zoomtrans.OvertakeZoom(zoomtrans.Well{W: 1, H: 1}, r.W, r.H, cellPx)
	newViewZoom := zoomtrans.IntrinsicFromLive(p.Zoom, overtake)
	if newViewX == int64(pl.RootViewCx) && newViewY == int64(pl.RootViewCy) &&
		math.Abs(newViewZoom-pl.RootViewZoom) < 0.001 {
		return
	}
	for i := range a.plugins {
		if a.plugins[i].UUID == pl.UUID {
			a.plugins[i].RootViewCx = float64(newViewX)
			a.plugins[i].RootViewCy = float64(newViewY)
			a.plugins[i].RootViewZoom = newViewZoom
		}
	}
	req := &rpc.SetRootViewRequest{
		RootGridID: p.Anchor,
		Cx:         float64(newViewX),
		Cy:         float64(newViewY),
		Zoom:       newViewZoom,
	}
	a.postVoidPersist("SetRootView", p.Anchor, func(ctx context.Context) error {
		return a.cl.SetRootView(ctx, req)
	})
}

// portalWellForFrame finds the link tile the pane descended through: the well
// in frame f's leaf grid whose child grid is the pane's current anchor. Nil
// when that grid isn't cached or the tile is gone (the ascent then pops
// instantly instead of animating).
func (a *App) portalWellForFrame(p *pane.Pane, f pane.Frame) *rpc.Tile {
	parentGridID := a.gridIDForPathFrom(f.Anchor, f.Path)
	if parentGridID == "" {
		return nil
	}
	g, ok := a.c.Grid(parentGridID)
	if !ok {
		a.fetchGrid(parentGridID)
		return nil
	}
	for _, t := range g.Tiles {
		if t.ChildGridID == p.Anchor && rpc.IsWellKind(t.Kind) {
			w := t
			return &w
		}
	}
	return nil
}

// animatePortalAscent zooms the pane out of the portal's grid back onto the
// link tile it descended through — the inverse of the portal descent. It
// mirrors startAscent's two-segment motion (child zoom-out to the calibrated
// swap state, then an atomic anchor swap and a parent pan+zoom to the saved
// viewport).
func (a *App) animatePortalAscent(p *pane.Pane, f pane.Frame, well *rpc.Tile) {
	r := paneRectFor(a, p)
	w := zoomtrans.Well{
		ID: well.ID, X: well.X, Y: well.Y, W: well.W, H: well.H,
		ViewX: well.ViewX, ViewY: well.ViewY, ViewZoom: well.ViewZoom,
	}

	from := zoomtrans.Endpoints{Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom}
	mid, to := zoomtrans.Ascent(from, w, nil, r.W, r.H, cellPx)
	to.Cx, to.Cy = float64(well.X)+float64(well.W)/2, float64(well.Y)+float64(well.H)/2
	saved := zoomtrans.Endpoints{Cx: f.Cx, Cy: f.Cy, Zoom: f.Zoom}

	childDist := panDist(mid.Cx-from.Cx, mid.Cy-from.Cy, from.Zoom) +
		zoomDist(from.Zoom, mid.Zoom)
	parentDist := panDist(saved.Cx-to.Cx, saved.Cy-to.Cy, saved.Zoom) +
		zoomDist(to.Zoom, saved.Zoom)
	durations := anim.SplitN([]float64{childDist, parentDist}, totalTransitionMs)

	p.DropFrame() // the transition (below) drives the restore, not an instant pop
	menuOpen := f.MenuOpen
	a.startTransition(&paneTransition{
		paneID:      p.ID,
		traceTileID: well.ID,
		segments: []transSegment{
			// Child: the plugin's root grid zooms out to the calibrated swap.
			{
				path:   nil,
				fromCx: from.Cx, fromCy: from.Cy, fromZoom: from.Zoom,
				toCx: mid.Cx, toCy: mid.Cy, toZoom: mid.Zoom,
				durationMs: durations[0],
			},
			// Parent: swap the anchor back to the frame's namespace (and its
			// path), then pan+zoom to the saved viewport.
			{
				setAnchor: true, anchor: f.Anchor,
				path:   slices.Clone(f.Path),
				fromCx: to.Cx, fromCy: to.Cy, fromZoom: to.Zoom,
				toCx: saved.Cx, toCy: saved.Cy, toZoom: saved.Zoom,
				durationMs: durations[1],
			},
		},
		onComplete: func() {
			if !menuOpen {
				return
			}
			a.menu.Open(p.ID)
			a.draw()
		},
	})
	a.scheduleURLUpdate()
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
	// actually animate from — the tested gridpath.AscentWalk; this closure
	// only resolves one level (cache lookup + a background fetch on a miss)
	// and captures the resolved well row.
	var well rpc.Tile
	level := gridpath.AscentWalk(p.Path, func(parentPath []string, wellID string) bool {
		parentGridID := a.gridIDForPathFrom(p.Anchor, parentPath)
		g, ok := a.c.Grid(parentGridID)
		if !ok {
			a.fetchGrid(parentGridID)
			return false
		}
		w, ok := g.Tiles[wellID]
		if !ok {
			return false
		}
		well = w
		return true
	})
	switch gridpath.ClassifyAscent(level, len(p.Path)) {
	case gridpath.AscentToRoot:
		a.instantAscend(p, nil)
		return
	case gridpath.AscentSnapToLevel:
		// We skipped one or more missing levels. The animation math
		// expects to be ascending out of the leaf grid; jumping mid-
		// path would render badly. Snap directly to the resolved level
		// and let the user ascend again from there if they want.
		a.instantAscend(p, slices.Clone(p.Path[:level]))
		return
	}
	parentPath := slices.Clone(p.Path[:level])
	r := paneRectFor(a, p)

	// Persist the user's current center as the well's view region so
	// the parent-grid preview reflects where they were when they left.
	// Mutates `well` in-place and updates the cache; queues the RPC
	// in a goroutine. Done before calibrating the ascent transition so
	// the path-swap point matches the user's actual position rather
	// than snapping back to the well's stored origin.
	a.saveWellViewBeforeAscent(p, &well, parentPath)

	from := zoomtrans.Endpoints{
		Path: slices.Clone(p.Path),
		Cx:   p.Cx, Cy: p.Cy, Zoom: p.Zoom,
	}
	w := zoomtrans.Well{
		ID: well.ID, X: well.X, Y: well.Y, W: well.W, H: well.H,
		ViewX: well.ViewX, ViewY: well.ViewY, ViewZoom: well.ViewZoom,
	}
	mid, switchTo := zoomtrans.Ascent(from, w, parentPath, r.W, r.H, cellPx)

	saved := a.popPaneState(p.ID)
	if saved == nil {
		// No in-session saved state (e.g., user reloaded mid-descent and
		// is now ascending). Land on the parent well centered at zoom 1.
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
		paneID:      p.ID,
		traceTileID: well.ID,
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
		onComplete: func() {
			if fp := a.tree.FindPane(p.ID); fp != nil {
				a.restoreStashedDescent(fp, saved)
			}
		},
	})
}

// instantAscend is the fallback path when the parent grid isn't cached or
// the well row vanished. We just drop the last entry of the path; the user
// can wait for the parent to load and reposition manually.
func (a *App) instantAscend(p *pane.Pane, parentPath []string) {
	// Still an ascent: flush the leaf framing if its parent is resolvable
	// (issue #190 — these fallback branches used to skip the writeback the
	// animated path performs, losing the viewport on an actual ascent).
	a.persistPaneFraming(p)
	a.popPaneState(p.ID) // discard whatever was saved; we can't honor it.
	p.Path = parentPath
	p.Cx, p.Cy, p.Zoom = 0, 0, 1.0
	a.clearSelected(p.ID)
	a.draw()
	a.scheduleURLUpdate()
}

// startDescent pushes the pane's current state onto the saved-state stack
// and installs a multi-segment transition into the well's child grid.
//
// Phases:
//
//	A. Combined pan+zoom in parent to (wellCenter, OvertakeZoom).
//	B. Atomic install of the calibrated child state at the path swap.
//	C. (Optional) animate the child to the well's stored ViewZoom so
//	   re-descent lands at the same zoom the user left at. Only fires
//	   when well.ViewZoom > 0; the default for never-entered wells is
//	   0 (calibrated zoom).
//
// Total time is split between A and C proportional to motion distance
// so neither feels rushed. C is zero-length when ViewZoom is unset.
func (a *App) startDescent(p *pane.Pane, well *rpc.Tile) {
	if well.ChildGridID == "" {
		// A link tile whose target isn't available — a broken or rootless
		// plugin on the node grid. Say why instead of silently doing nothing
		// (charter §6); pluginhealth owns the wording when it knows the plugin.
		if pl, ok := a.pluginByUUID(rpc.LocalOf(well.ID)); ok {
			if sev, source, message, ok := pluginhealth.ClickNotice(pl); ok {
				a.reportErr(sev, source, message)
				return
			}
		}
		// A still-unconfigured schema-kind tile (a connection well dropped
		// bare — issue #209): the first descent is where the parameters get
		// asked for.
		if a.openConfigureTile(p, well) {
			return
		}
		a.reportErr(errsurface.Info, "descend", "nothing to descend into: "+well.AltText)
		return
	}
	r := paneRectFor(a, p)
	from := zoomtrans.Endpoints{
		Path: slices.Clone(p.Path),
		Cx:   p.Cx, Cy: p.Cy, Zoom: p.Zoom,
	}
	w := zoomtrans.Well{
		ID: well.ID, X: well.X, Y: well.Y, W: well.W, H: well.H,
		ViewX: well.ViewX, ViewY: well.ViewY, ViewZoom: well.ViewZoom,
	}
	if isLinkTile(well) {
		// A LINK crosses into another id space (a plugin tile on the node
		// grid, a mounted well, a cross-plugin clone). Descend as a PORTAL:
		// push the current level onto the ascent stack and swap the pane's
		// anchor to the link's target, so the URL and every path id stay
		// within one anchor's namespace. Ascent pops the frame and lands
		// back on this tile.
		// The pane.Up FRAME is the one owner of the portal return (anchor,
		// path, viewport, menu). Deliberately NOT pushed onto the session
		// ascent stack: portal ascent pops the frame only, so a second copy
		// there would be orphaned — and a boot-descended pane (whose stack
		// starts empty) could later mis-consume the orphan as a well-ascent
		// viewport. Two stacks, two disjoint owners: frames own namespace
		// CROSSINGS, the session stack owns in-namespace well/file descents.
		wasMenu := a.menu.OpenOn(p.ID)
		a.menu.Close()
		p.PushFrame(wasMenu)
		a.installDescent(p, r, from, w, well.ChildGridID, well.ChildGridID,
			float64(well.X)+float64(well.W)/2, float64(well.Y)+float64(well.H)/2)
		return
	}
	a.pushPaneState(p.ID, paneState{Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom})
	a.installDescent(p, r, from, w, well.ChildGridID, "", 0, 0)
}

// installDescent computes the standard two-segment descent transition into a
// well's child grid and starts it. A portal jump (portalAnchor != "") swaps
// the pane's plugin anchor at the path swap instead of appending to the path,
// and recentres the parent zoom on (portalCx, portalCy) — the launcher tile,
// or the pane centre for an in-grid menu — so that is what grows to fill the
// pane. Same machinery as a same-plugin well descent, so the motion is
// identical.
func (a *App) installDescent(p *pane.Pane, r pane.Rect, from zoomtrans.Endpoints, w zoomtrans.Well, childGridID, portalAnchor string, portalCx, portalCy float64) {
	mid, swap, final := zoomtrans.Descent(from, w, r.W, r.H, cellPx)
	if portalAnchor != "" {
		// The synthetic well's integer cell rounds the launcher tile's
		// position; recentre the parent zoom on the exact footprint center so
		// the descent lands square on the tile.
		mid.Cx, mid.Cy = portalCx, portalCy
	}
	a.fetchGrid(childGridID)

	parentDist := panDist(mid.Cx-from.Cx, mid.Cy-from.Cy, from.Zoom) +
		zoomDist(from.Zoom, mid.Zoom)
	childDist := zoomDist(swap.Zoom, final.Zoom)
	var durations []float64
	if childDist > 0 {
		durations = anim.SplitN([]float64{parentDist, childDist}, totalTransitionMs)
	} else {
		durations = []float64{totalTransitionMs, 0}
	}

	// C: after the atomic swap, ease the child zoom out to the stored ratio
	// (zero-length when swap == final). A portal swaps the plugin anchor and
	// resets the path to the plugin root instead of appending a well id.
	segC := transSegment{
		path:   swap.Path,
		fromCx: swap.Cx, fromCy: swap.Cy, fromZoom: swap.Zoom,
		toCx: final.Cx, toCy: final.Cy, toZoom: final.Zoom,
		durationMs: durations[1],
	}
	if portalAnchor != "" {
		segC.setAnchor = true
		segC.anchor = portalAnchor
		segC.path = nil
	}

	a.startTransition(&paneTransition{
		paneID: p.ID,
		segments: []transSegment{
			// A: parent pan+zoom toward the well/footprint center at Overtake.
			{
				path:   from.Path,
				fromCx: from.Cx, fromCy: from.Cy, fromZoom: from.Zoom,
				toCx: mid.Cx, toCy: mid.Cy, toZoom: mid.Zoom,
				durationMs: durations[0],
			},
			segC,
		},
	})
}

// startTextDescent zooms a pane into a text tile in a single
// concurrent pan+zoom motion, then flips to text-editing mode. Unlike
// well descent, the path is not extended (the tile lives in the parent
// grid as a leaf tile) and the meaningful screen area in live mode is
// the inner box (textarea region), not the full pane — so the descent
// targets TextOvertake (parent zoom that makes the footprint fit the
// inner box), one notch further in than the legacy OvertakeZoom. At
// the path-swap, the footprint screen size = inner-box, and the live
// TextZoom is reconstructed from the tile's intrinsic ViewZoom ratio
// for visual continuity.
//
// afterDescend, if non-nil, is called after the transition completes and
// TextFocus has been installed. Use this to chain actions that need the
// pane to be fully descended (e.g., opening the URL stream after a new
// URL tile is created).
func (a *App) startTextDescent(p *pane.Pane, file *rpc.Tile, afterDescend func()) {
	a.pushPaneState(p.ID, paneState{Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom})

	r := paneRectFor(a, p)
	from := zoomtrans.Endpoints{
		Path: slices.Clone(p.Path),
		Cx:   p.Cx, Cy: p.Cy, Zoom: p.Zoom,
	}
	wellCx := float64(file.X) + float64(file.W)/2
	wellCy := float64(file.Y) + float64(file.H)/2
	target := textFitZoom(r, file.W, file.H)
	if target < from.Zoom {
		target = from.Zoom
	}

	// Eagerly fetch the blob so it's likely cached by the time the
	// transition lands. URL tiles don't have a blob; their preview
	// path goes through urlPreview instead.
	if file.Kind == rpc.KindText {
		a.fetchTileContent(file.ID)
		// Source-backed text tiles (the @info tile in a proc-well, fs
		// file metadata) are reconciled server-side from live host
		// state. Trigger a parent GetGrid so the reconciler runs again
		// — the response (and the TileChanged event it fires) repoints
		// the tile at the freshest blob, which the next render frame
		// fetches automatically. The user briefly sees the previous
		// snapshot, then it snaps to current.
		if a.tileReadOnly(file) {
			a.fetchGrid(a.gridIDForPane(p))
		}
	}

	fileID := file.ID
	// Captured by value for the auto-live decision: an ephemeral (scratch-
	// grid) tile is in no cached grid, so a cache lookup at transition end
	// would miss it and silently skip going live.
	fileCopy := *file
	initialScroll := float64(file.TextY)
	initialScrollX := float64(file.TextX)
	// URL tiles have no text/rendered modes; mode is "" for them so
	// the textarea overlay (gated on TextMode == "text") never shows.
	// For text tiles the mode is the one persisted on the tile (server),
	// defaulting to raw text for a never-opened tile. Source-backed text
	// tiles (the @info tile, fs file metadata) are read-only — descent
	// always shows the rendered markdown so the user never sees a
	// blinking caret over content they can't change.
	var mode string
	if file.Kind == rpc.KindText {
		if a.tileReadOnly(file) {
			mode = rpc.TextModeRendered
		} else {
			mode = file.TextMode
			if mode == "" {
				mode = rpc.TextModeText
			}
		}
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
			fp.TextFocus = fileID
			fp.TextMode = mode
			fp.TextScrollY = initialScroll
			fp.TextScrollX = initialScrollX
			fp.TextZoom = a.textScaleFor(fp) // base × content zoom (issue #82)
			// Unsaved-edit state is NOT touched here: it lives tile-scoped
			// in the content store, so descending this pane elsewhere can't
			// strand a previous document's typing.
			a.refreshFileOverlay()
			// Descending IS the engagement gesture (owner decision
			// 2026-07-26, issue #202): a url reopens, a shell reconnects
			// (or creates, when fresh). The decision lives HERE, once —
			// call sites no longer hand-roll go-live callbacks, so every
			// door into a descent behaves identically. afterDescend
			// remains for the callers that need something ELSE to run
			// after the swap (none currently go live by hand).
			a.autoLiveOnDescent(fp.ID, &fileCopy)
			if afterDescend != nil {
				afterDescend()
			}
		},
	})
}

// autoLiveOnRestore is autoLiveOnDescent for the RESTORE paths — reload
// (applyURLState), workspace install, an ascent landing back on a stashed
// descent (restoreStashedDescent). The tile row is not necessarily cached at
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
	if fp == nil || fp.TextFocus != tile.ID {
		return
	}
	if a.gridIDForPathFrom(fp.Anchor, fp.Path) == tile.GridID {
		return
	}
	res, err := a.cl.Search(context.Background(), "id:"+tile.ID, tile.ID, 1)
	if err != nil || len(res) == 0 {
		return
	}
	wells := res[0].Path
	fp = a.tree.FindPane(paneID)
	if fp == nil || fp.TextFocus != tile.ID {
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
	fp.Anchor = anchor
	fp.Path = path
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
	if fp == nil || fp.TextFocus != tileID {
		return
	}
	// The shell facts key by the CONTENT id (a link attaches its target's
	// session) — the same reads shellRefreshButtonVisible does, so the two
	// decisions can never disagree about a dead session.
	cid := tile.ContentID()
	alive, known := a.shellAlive[cid]
	verdict := shellconn.DecideAutoLive(
		tile.Kind == rpc.KindURL, tile.Kind == rpc.KindShell,
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
			if p := a.tree.FindPane(paneID); nowAlive && p != nil && p.TextFocus == tileID {
				a.openShellStream(p, tileID)
			}
		})
	}
}

// restoreStashedDescent applies a saved paneState's stacked-descent fields to
// fp when an ascent's saved state carries a TextFocus — the descent the pane
// was in when a deeper ephemeral visit was stacked on top of it
// (descendEphemeral: a url opened over a live shell descent). One ascent
// lands back on the stashed descent — and, for a cross-grid stash, its
// anchor + path — rather than in the grid behind it. No-op when the saved
// state carries no focus (an ordinary ascent).
func (a *App) restoreStashedDescent(fp *pane.Pane, saved *paneState) {
	if saved == nil || saved.TextFocus == "" {
		return
	}
	if saved.Anchor != "" {
		fp.Anchor = saved.Anchor
		fp.Path = slices.Clone(saved.Path)
	}
	fp.TextFocus = saved.TextFocus
	fp.TextMode = saved.TextMode
	fp.TextScrollX = saved.TextScrollX
	fp.TextScrollY = saved.TextScrollY
	fp.TextZoom = a.textScaleFor(fp) // base × content zoom (issue #82)
	a.refreshFileOverlay()
	// Landing back on a stashed url/shell descent re-engages it (issue
	// #202) — the same one-owner decision every descent applies.
	a.autoLiveOnRestore(fp.ID, fp.TextFocus)
}

func (a *App) startTextAscent(p *pane.Pane) {
	if p.TextFocus == "" {
		return
	}
	// descendedTile resolves an ephemeral url visit (focused off the pane's grid),
	// so its ascent animates like any other rather than snapping instantly.
	file, ok := a.descendedTile(p)
	if !ok {
		a.exitFileFocusInstant(p)
		return
	}
	r := paneRectFor(a, p)
	wellCx := float64(file.X) + float64(file.W)/2
	wellCy := float64(file.Y) + float64(file.H)/2
	overtake := textFitZoom(r, file.W, file.H)
	if overtake > p.Zoom {
		overtake = p.Zoom
	}

	saved := a.popPaneState(p.ID)
	if saved == nil {
		saved = &paneState{Cx: wellCx, Cy: wellCy, Zoom: 1.0}
	}

	// Save before transition: capture the editor buffer (if text mode is
	// active) and post UpdateText + SetTextView. The animation runs
	// concurrently with the network round-trip; the user doesn't have
	// to wait.
	a.saveTextBeforeAscent(p, file)

	// Ascending out of an EPHEMERAL tile deletes it — gray means gone
	// (issue #85): no freeze (pointless for a tile about to die, and a url
	// freeze would bump the version out from under the delete), then the
	// row goes away (for a shell, the plugin kills its tmux session too).
	ephemeral := a.isEphemeralTile(p, &file) && !a.otherPaneShowsTile(p.ID, file.ID)

	// If we're ascending out of a URL tile, close the live stream (if any).
	if file.Kind == rpc.KindURL {
		a.closeURLStream(p.ID, !ephemeral)
	}
	// Shell ascent: capture the JPEG, persist it as the frozen
	// preview, close the WS. closeShellStream handles all three.
	if file.Kind == rpc.KindShell {
		a.closeShellStream(p.ID, !ephemeral)
	}
	if ephemeral {
		a.deleteEphemeralTile(file.ID, file.Version)
	}

	// (Mode + framed window are persisted by saveTextBeforeAscent, which
	// also patches the cache so the preview is correct immediately.)

	// Reset parent-grid zoom to the overtake value so the animation
	// begins from "well filling the pane", regardless of how the user
	// zoomed within the text tile. Then clear TextFocus so the chrome (toggle
	// button, textarea) goes away as the animation begins.
	p.Zoom = overtake
	p.Cx, p.Cy = wellCx, wellCy
	p.TextFocus = ""
	a.refreshFileOverlay()

	// If the saved state had a TextFocus, a deeper ephemeral visit was
	// stacked over that descent — restore it (and, for a cross-grid
	// follow, its anchor + path) as the ascent landing on complete.
	a.startTransition(&paneTransition{
		paneID:      p.ID,
		traceTileID: file.ID,
		segments: []transSegment{
			// Single combined pan+zoom segment back to the saved viewport.
			{
				path:   slices.Clone(p.Path),
				fromCx: wellCx, fromCy: wellCy, fromZoom: overtake,
				toCx: saved.Cx, toCy: saved.Cy, toZoom: saved.Zoom,
				durationMs: totalTransitionMs,
			},
		},
		onComplete: func() {
			if fp := a.tree.FindPane(p.ID); fp != nil {
				a.restoreStashedDescent(fp, saved)
			}
		},
	})
}

// exitFileFocusInstant is the fallback path when the parent grid isn't
// cached or the text tile row vanished while we were focused on it. We just
// clear TextFocus and reset the viewport to whatever was saved.
func (a *App) exitFileFocusInstant(p *pane.Pane) {
	a.exitTextInstant(p, true)
	a.draw()
	a.scheduleURLUpdate()
}

// exitTextInstant pops a text/url/shell descent with no animation,
// performing the same saves and stream teardown the animated ascent does.
// restoreStash controls whether a stashed descent is restored (a single
// ascent lands back on it) or consumed and discarded (a multi-level crumb
// jump is heading ABOVE the stash origin — bottombar.go).
func (a *App) exitTextInstant(p *pane.Pane, restoreStash bool) {
	// The freeze is kept here (streams may be live) except for an ephemeral
	// tile, which is deleted instead — same rule as startTextAscent.
	ephemeral := false
	if t, ok := a.descendedTile(p); ok {
		// The buffer/framing save the animated path performs — resolvable
		// rows get it here too, so an instant pop never loses an edit.
		a.saveTextBeforeAscent(p, t)
		if a.isEphemeralTile(p, &t) && !a.otherPaneShowsTile(p.ID, t.ID) {
			ephemeral = true
			defer a.deleteEphemeralTile(t.ID, t.Version)
		}
	}
	a.closeURLStream(p.ID, !ephemeral)   // no-op if not a URL descent
	a.closeShellStream(p.ID, !ephemeral) // no-op if not a shell descent
	saved := a.popPaneState(p.ID)
	p.TextFocus = ""
	if saved != nil {
		p.Cx, p.Cy, p.Zoom = saved.Cx, saved.Cy, saved.Zoom
		// If the saved state captured a stacked text descent, restore it
		// (and its anchor/path for a cross-grid follow) so a single ascent
		// lands on it, not the grid behind it.
		if restoreStash {
			a.restoreStashedDescent(p, saved)
		}
	}
	a.refreshFileOverlay()
}

// saveWellViewBeforeAscent is persistWellView under the pane's own anchor —
// the ascent-flush entry point (the settle persister passes an explicit
// anchor because a portal's containing well lives under the FRAME's anchor).
func (a *App) saveWellViewBeforeAscent(p *pane.Pane, well *rpc.Tile, parentPath []string) {
	a.persistWellView(p, well, p.Anchor, parentPath)
}

// persistWellView updates `well`'s ViewX/ViewY/ViewZoom so its parent-grid
// preview reflects the user's last position and zoom in the child grid.
// Fired by every ascent flush and by the settle persister
// (flushFramingSave). ViewZoom is stored as the intrinsic ratio
// childZoom_at_ascent / OvertakeZoom_at_ascent — window-independent so
// the preview stays stable across browser resizes. Mutates well in-place
// (so the local-side ascent transition uses the new values) and patches
// the cache so the parent's preview renders the new view immediately on
// path-swap. Posts SetWellView in a goroutine; the server's event will
// catch up the cache.
//
// No-op if the user's current center hasn't moved from the well's
// stored view (rounded to int cells), so quiet calls don't churn
// the DB.
func (a *App) persistWellView(p *pane.Pane, well *rpc.Tile, parentAnchor string, parentPath []string) {
	// Quantize the ORIGIN, not the center: descend targets origin + size/2,
	// and rounding that half-cell center drifted the stored window one cell
	// per untouched round trip (zoomtrans.ViewOriginFromCenter has the
	// arithmetic and its idempotence property test).
	newViewX := zoomtrans.ViewOriginFromCenter(p.Cx, well.W)
	newViewY := zoomtrans.ViewOriginFromCenter(p.Cy, well.H)
	r := paneRectFor(a, p)
	zw := zoomtrans.Well{X: well.X, Y: well.Y, W: well.W, H: well.H}
	overtake := zoomtrans.OvertakeZoom(zw, r.W, r.H, cellPx)
	newViewZoom := zoomtrans.IntrinsicFromLive(p.Zoom, overtake)
	if newViewX == well.ViewX && newViewY == well.ViewY &&
		math.Abs(newViewZoom-well.ViewZoom) < 0.001 {
		return
	}
	well.ViewX = newViewX
	well.ViewY = newViewY
	well.ViewZoom = newViewZoom

	// Update local cache so the parent preview renders the new view
	// before the SSE event from the server arrives.
	updated := *well
	a.c.Apply(rpc.Event{
		Kind:        rpc.EventTileChanged,
		TileChanged: &rpc.TileChanged{Tile: updated},
	})

	parentGridID := a.gridIDForPathFrom(parentAnchor, parentPath)
	req := &rpc.SetWellViewRequest{
		TileID:   well.ID,
		Version:  well.Version,
		ViewX:    newViewX,
		ViewY:    newViewY,
		ViewZoom: newViewZoom,
	}
	// Optimistic dispatcher: the cache patch above must roll back on ANY
	// failure, not just a conflict (issue #156).
	a.postOptimisticPersist("SetWellView", parentGridID, func(ctx context.Context) (*rpc.Tile, error) {
		return a.cl.SetWellView(ctx, req)
	})
}

// saveTextBeforeAscent posts the editor buffer (if text mode is active)
// and the live scroll position back to the server. Failures are silently
// dropped; the user will see the local state on next descent and the
// server state otherwise.
func (a *App) saveTextBeforeAscent(p *pane.Pane, file rpc.Tile) {
	// SetTextView (and the framed-window cache patch) are text-tile
	// concerns — URL and shell tiles don't carry text_x/text_y/text_w
	// /text_h, and the server's SetTextView rejects non-text kinds with
	// InvalidArgument. Routing them through would surface as a 400 plus
	// a spurious "version conflict" refetch in the wasm dispatcher.
	if file.Kind != rpc.KindText {
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
		// honor "however you left it" across reloads.
		req := &rpc.SetTextViewRequest{
			TileID:   file.ID,
			Version:  curVersion,
			TextX:    scrollX,
			TextY:    scrollY,
			TextW:    viewW,
			TextH:    viewH,
			TextMode: mode,
		}
		a.doTileMutate("SetTextView", gid, func(ctx context.Context) (*rpc.Tile, error) {
			return a.cl.SetTextView(ctx, req)
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

	// A plugin item drops an exit-well link to the plugin's root grid in
	// the destination grid (drag-a-plugin-onto-a-grid). Only writable
	// grids accept it; a read-only grid snaps it back.
	if d.item.isPlugin {
		if d.item.plugin.RootGridID == "" || !a.gridWritable(a.gridIDForPane(destPane)) {
			a.cancelDragSnapBack(d)
			return
		}
		targetX, targetY := dpscreen.CellToScreen(float64(dropX), float64(dropY))
		if a.ghost != nil {
			a.ghost.paneID = destPane.ID
		}
		a.startSnap(targetX, targetY, snapMs)
		a.createPluginLinkAtCell(destPane, d.item.plugin, dropX, dropY)
		a.menu.Close()
		return
	}

	// EVERY template commits immediately with the snap-and-create gesture
	// (issue #209: drop first — the drop never prompts; whatever a kind
	// needs to be useful is asked for on the first DESCENT, so create is
	// one experience everywhere: drop, descend, fill in).
	targetX, targetY := dpscreen.CellToScreen(float64(dropX), float64(dropY))
	if a.ghost != nil {
		a.ghost.paneID = destPane.ID
	}
	a.startSnap(targetX, targetY, snapMs)

	switch d.item.primitive {
	case tplWell:
		a.createWellAtCell(destPane, dropX, dropY)
	case tplMarkdown:
		a.createTextAtCell(destPane, []byte{}, dropX, dropY)
	case tplURL:
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
		ViewX:       int64(pl.RootViewCx),
		ViewY:       int64(pl.RootViewCy),
		ViewZoom:    pl.RootViewZoom,
	}
	a.postTileMutate("CreateWell", gid, func(ctx context.Context) (*rpc.Tile, error) {
		return a.cl.CreateWell(ctx, req)
	}, nil)
}

// createWellAtCell fires CreateWell at the given cell. Footprint is 1×1.
// Wells are created UNNAMED (naming happens from inside, via the name
// bubble — issue #118) and UNCONFIGURED: even on a grid whose plugin
// declares a creation schema (Grid.CreateSchemas, issue #198 — an ssh
// connection's user/host), the drop commits immediately and the parameter
// form opens on the first DESCENT instead (issue #209; a param-less
// connection well is a legal, inert state — dashed and childless until its
// params commit).
func (a *App) createWellAtCell(p *pane.Pane, cellX, cellY int64) {
	gid := a.gridIDForPane(p)
	req := &rpc.CreateWellRequest{
		GridID: gid, X: cellX, Y: cellY, W: 1, H: 1,
	}
	a.postTileMutate("CreateWell", gid, func(ctx context.Context) (*rpc.Tile, error) {
		return a.cl.CreateWell(ctx, req)
	}, nil)
}

// openConfigureTile opens the creation-schema form for a still-unconfigured
// tile on its first descent (issue #209: drop first, prompt on descent).
// Submit commits the params as the tile's CONTENT — the plugin validates
// authoritatively; a refusal surfaces and the empty tile stays visible and
// deletable, never silent. Returns false when the tile's grid declares no
// schema for its kind (the caller falls through to its generic notice);
// true when the prompt opened or an unrenderable schema was surfaced.
func (a *App) openConfigureTile(p *pane.Pane, t *rpc.Tile) bool {
	gid := a.gridIDForPane(p)
	form, ok := a.createSchemaFor(gid, t.Kind)
	if !ok {
		return true // unrenderable schema: already surfaced loudly
	}
	if form == nil {
		return false
	}
	id, version := t.ID, t.Version
	a.openSchemaModal("configure", form, func(params []byte) {
		if len(params) == 0 {
			return
		}
		go a.postWriteContent(gid, id, version, params)
	}, nil)
	return true
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
// descent (issue #209) — the url twin of openConfigureTile, reusing the
// url modal with its visited-url suggestions. Submit writes the address as
// the tile's content (the store's url arm: versioned, validated, bumps) and
// then descends, so the fill-in flows straight into the page. EVERY descent
// goes live (issue #202), so no special go-live handling.
func (a *App) openConfigureURL(p *pane.Pane, t *rpc.Tile) {
	gid := a.gridIDForPane(p)
	paneID, id, version := p.ID, t.ID, t.Version
	candidates := a.urlSuggestCandidates(uuidOf(gid))
	a.openURLModal(candidates, func(url string) {
		go func() {
			tile, ok := a.postWriteContent(gid, id, version, []byte(url))
			if !ok {
				return
			}
			fp := a.tree.FindPane(paneID)
			if fp == nil || fp.TextFocus != "" {
				return
			}
			a.startTextDescent(fp, &tile, nil)
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
			a.descendEphemeral(fp, &tile)
		}
	})
}

// descendEphemeral descends fp into the off-grid ephemeral tile (url or
// shell) and goes live. The pane keeps its grid — the tile is resolved by id
// (render / streams / ascent all use descendedTile) — so one ordinary ascent
// lands right back here, and the ascent DELETES the tile (issue #85): gone
// means gone, tmux session included. If fp is ALREADY descended (a live
// shell, from the shell-link click) that descent is stashed so one ascent
// returns to it — the underlying shell goes inactive, not gone — rather than
// clearing straight to the grid.
func (a *App) descendEphemeral(fp *pane.Pane, tile *rpc.Tile) {
	// Place change: flush framing still inside the settle window (issue
	// #190). A no-op when fp is already descended (TextFocus set).
	a.flushFramingSave()
	if fp.TextFocus == "" {
		a.startTextDescent(fp, tile, nil)
		return
	}
	// Stash the current descent (shell): restoreStashedDescent lands back on it.
	savedAnchor := fp.Anchor
	savedPath := slices.Clone(fp.Path)
	savedFocus := fp.TextFocus
	savedMode := fp.TextMode
	savedScrollX := fp.TextScrollX
	savedScrollY := fp.TextScrollY
	fp.TextFocus = ""
	fp.TextMode = ""
	a.refreshFileOverlay()
	a.startTextDescent(fp, tile, nil)
	if top := a.local(fp.ID).PeekAscent(); top != nil {
		top.Anchor = savedAnchor
		top.Path = savedPath
		top.TextFocus = savedFocus
		top.TextMode = savedMode
		top.TextScrollX = savedScrollX
		top.TextScrollY = savedScrollY
	}
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

// otherPaneShowsTile reports whether any pane OTHER than paneID is currently
// descended into tileID. Delete-on-ascent must not fire while another pane
// still shows the ephemeral visit — splitting an ephemeral descent clones the
// view, and the clone's file-level ascent would otherwise delete the tile out
// from under the source pane (found while building issue #111).
func (a *App) otherPaneShowsTile(paneID, tileID string) bool {
	found := false
	a.tree.Walk(func(p *pane.Pane) {
		if p.ID != paneID && p.TextFocus == tileID {
			found = true
		}
	})
	return found
}

// deleteEphemeralTile removes an ascended-from ephemeral tile — gray means
// gone: the row is deleted, and for a shell the plugin kills its tmux session
// (all processes) as part of the delete. A failure surfaces on the strip
// (charter §6); otherwise the tile would silently leak in the scratch grid
// until the startup sweep.
func (a *App) deleteEphemeralTile(tileID string, version int64) {
	go func() {
		err := a.cl.DeleteTile(context.Background(), &rpc.DeleteTileRequest{
			TileID: tileID, Version: version,
		})
		if err != nil && isVersionConflict(err) {
			// The stream close that precedes this delete triggers the plugin's
			// detach-time title capture — a version bump racing the claim
			// (deterministically since the gRPC transport made teardown fast,
			// 2026-07-26). The delete is the USER's explicit action and an
			// automatic capture must never outrank it: retry once with a
			// fresh claim.
			if fresh, gerr := a.cl.GetTile(context.Background(), tileID); gerr == nil {
				err = a.cl.DeleteTile(context.Background(), &rpc.DeleteTileRequest{
					TileID: tileID, Version: fresh.Version,
				})
			}
		}
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
			a.descendEphemeral(fp, &tile)
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
	if r := paneRectFor(a, p); r.H < 2*pane.MinPanePx {
		a.visitEphemeralURL(p, url)
		return
	}
	newP, err := a.tree.SplitOnSideAt(pane.SideBottom, 0.5)
	if err != nil {
		a.visitEphemeralURL(p, url)
		return
	}
	// The clone inherits the source's content descent (TextFocus), which a
	// live view can't duplicate — same rule as commitSplit: ascend the file
	// level so the visit descends from the containing grid. (The ephemeral
	// delete-on-ascent is guarded by otherPaneShowsTile, so this ascent
	// never deletes the tile the SOURCE pane still shows.)
	if newP.TextFocus != "" {
		a.startTextAscent(newP)
	}
	a.draw()
	a.scheduleURLUpdate()
	a.visitEphemeralURL(newP, url)
}

// shellURLActivate handles a click on an http(s) url in a live shell (the xterm
// link provider's activate): open it below, exactly like a link a live url
// view pops (issue #207 — one behavior for links out of live tiles; the old
// in-place descent stacked the shell on the session-only ascent stash, which
// any place-restore dropped — the issue #208 double-ascend). A no-op if the
// shell is no longer the pane's active descent.
func (a *App) shellURLActivate(paneID, url string) {
	if p := a.tree.FindPane(paneID); p != nil && p.TextFocus != "" {
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
