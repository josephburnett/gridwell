//go:build js && wasm

package main

import (
	"context"
	"math"
	"slices"
	"syscall/js"

	"github.com/josephburnett/gridwell/client/anim"
	"github.com/josephburnett/gridwell/client/dragdrop"
	"github.com/josephburnett/gridwell/client/gridpath"
	"github.com/josephburnett/gridwell/client/pane"
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
	// Window-level keyboard listeners forward every keystroke to the
	// remote URL stream whenever the focused pane is descended into a
	// URL tile. Window-level (not canvas-level) so we catch keys
	// regardless of where in the document focus happens to sit.
	a.win.Call("addEventListener", "keydown", js.FuncOf(a.onKeyDown))
	a.win.Call("addEventListener", "keyup", js.FuncOf(a.onKeyUp))
	// Touch screens: translate single-finger touch into the same mouse
	// gestures (see touch.go).
	a.installTouchInput()
}

// onKeyDown / onKeyUp are no-ops. When a pane is descended into a live URL
// tile, the native WebContentsView floated over the content box has OS
// keyboard focus and handles its own input — the gridwell canvas never sees
// those keystrokes. The handlers remain registered (and harmlessly inert) so
// the listener wiring in installCanvasInput is unchanged.
func (a *App) onKeyDown(this js.Value, args []js.Value) any { return nil }

func (a *App) onKeyUp(this js.Value, args []js.Value) any { return nil }

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
	gid := a.gridIDForPath(p.Path)
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

// updateURLCursor sets the canvas CSS cursor for a URL descent pane.
// Frozen descent: "grab" at rest, "grabbing" while dragging.
// Live descent: default (the iframe / Chromium manages its own cursor).
func (a *App) updateURLCursor(p *pane.Pane, _ pane.Rect) {
	if a.urlStreams[p.ID] != nil {
		// Live: restore default and let the page/browser control the cursor.
		a.canvas.Get("style").Set("cursor", "")
		return
	}
	// Frozen: grab while hovering content area.
	if a.urlPanDragging {
		a.canvas.Get("style").Set("cursor", "grabbing")
	} else {
		a.canvas.Get("style").Set("cursor", "grab")
	}
}

func (a *App) onWheel(this js.Value, args []js.Value) any {
	args[0].Call("preventDefault")
	dy := args[0].Get("deltaY").Float()
	sx, sy := mouseXY(args[0], a.canvas)
	p, r, ok := a.paneAtScreen(sx, sy)
	if !ok {
		return nil
	}
	// Inside a focused text tile the wheel rules differ by region. Outer
	// ring (the visible grid pattern) zooms TextZoom centered on the
	// cursor; inner area scrolls (rendered mode) or is handled by the
	// textarea natively (text mode — those events never reach the
	// canvas listener).
	if p.TextFocus != "" {
		// URL descent: a live tile is a native WebContentsView over the
		// content box and scrolls itself (the canvas never sees those wheel
		// events). When live, swallow any stray wheel that does reach the
		// canvas so it doesn't zoom the pane underneath; otherwise fall
		// through to the pane-wide gridwell wheel (zoom) like other files.
		if a.isURLDescent(p) {
			if a.urlStreams[p.ID] != nil && pointInPaneContent(r, sx, sy) {
				args[0].Call("preventDefault")
				return nil
			}
		}
		// Text file: fixed scale, no zoom. The wheel only scrolls the
		// window vertically (rendered mode). In text mode the textarea
		// overlay handles its own scrolling and the wheel never reaches
		// the canvas.
		if p.TextMode == rpc.TextModeRendered {
			p.TextScrollY += dy
			if p.TextScrollY < 0 {
				p.TextScrollY = 0
			}
			a.draw()
			a.scheduleURLUpdate()
		}
		return nil
	}
	// Smooth zoom centered on the cursor: amount scales with deltaY so a
	// fast scroll covers more range, but capped per event. The pane's
	// (Cx, Cy) shifts so the world point under the cursor stays under
	// the cursor after the zoom — like every map app.
	ps := paneToDragdrop(p, r)
	cellX, cellY := ps.ScreenToCell(sx, sy)
	// Step clamp + cursor-anchored re-center is the pure zoomtrans.WheelZoom.
	p.Zoom, p.Cx, p.Cy = zoomtrans.WheelZoom(dy, p.Zoom, p.Cx, p.Cy, cellX, cellY, zoomFactor, zoomMin, zoomMax)
	a.draw()
	a.scheduleURLUpdate()
	a.scheduleRootViewSave()
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
		// the textarea stranded. An open + menu belongs to the pane that
		// was focused; close it so it doesn't outlive its (now hidden) +
		// button on the de-focused pane.
		a.menuOpen = false
		a.refreshFileOverlay()
		a.draw()
	}
	button := args[0].Get("button").Int()
	if button == 2 {
		args[0].Call("preventDefault")
		a.onRightDown(p, r, sx, sy)
		return nil
	}
	if button == 1 {
		// Middle (third) button ascends the pane under the cursor. The
		// edge band no longer ascends; this is one of the two new ascent
		// gestures (the other is right-click on the corner circle).
		// preventDefault suppresses the browser's middle-click autoscroll.
		args[0].Call("preventDefault")
		a.menuOpen = false
		a.ascendPane(p)
		return nil
	}
	if button != 0 {
		return nil
	}

	// The creation palette floats above every pane, so a click in its
	// popover wins — even where it overflows a neighbouring pane (which
	// would otherwise pan/grab). Resolve against the MENU pane, not the
	// pane under the cursor, so grabbing a swatch from the overflow region
	// starts a template drag instead of scrolling the pane beneath.
	if a.menuOpen {
		if mp := a.tree.FindPane(a.menuPaneID); mp != nil {
			mr := paneRectFor(a, mp)
			if pointInPalette(mp, mr, sx, sy) {
				if idx := paletteTileIndexAt(mp, mr, sx, sy); idx >= 0 {
					a.startTemplateDrag(mp, mr, idx, sx, sy)
				}
				return nil // swallow; missing a swatch keeps the palette open
			}
		}
	}

	// Left-drag on a pane boundary resizes the divider — same divider math
	// as the right-button resize, but it never closes a pane (clamped to a
	// recoverable minimum). Checked first so a grab near the edge wins over
	// content interactions. No divider on that side → falls through.
	if a.armLeftResize(p, r, sx, sy) {
		return nil
	}

	// In file-focus mode the lower-right button is a text/rendered toggle
	// rather than the + creation menu.
	if p.TextFocus != "" {
		// Shell tile descent: the plus button refreshes a frozen
		// shell (spawns a new bash), and the outer margin ascends.
		// Inside the xterm overlay, clicks are captured by xterm.js
		// itself (the DOM element absorbs them), so this path only
		// fires for clicks on the pane chrome.
		if a.isShellDescent(p) {
			// The corner refresh button only acts on the already-focused
			// pane (it's hidden otherwise). A click here on a just-focused
			// pane falls through to the content/xterm path below.
			if prevFocus == p.ID && pointInPlus(r, sx, sy) && !a.hasShellStream(p.ID) {
				gid := a.gridIDForPath(p.Path)
				if g, ok := a.c.Grid(gid); ok {
					if tile, ok := g.Tiles[p.TextFocus]; ok && a.shellRefreshButtonVisible(&tile) {
						a.openShellStream(p, tile.ID)
					}
				}
				return nil
			}
			if !pointInPaneContent(r, sx, sy) {
				// Outer margin: ascent moved to the middle button / a
				// right-click on the corner circle. Swallow so a margin
				// click doesn't start anything.
				return nil
			}
			// Inside the content area: clicks fall through to xterm
			// (the overlay div is above the canvas). Live shell
			// streams never receive clicks here in practice.
			return nil
		}
		// URL tile descent. A live tile is a native WebContentsView over the
		// content box; it owns its own clicks (the canvas never sees them).
		// The canvas only gets events here when the pane is frozen, or in the
		// pane-border band. The outer margin ascends; the corner button goes
		// live (frozen) or navigates back (live, if a stray click reaches us).
		if a.isURLDescent(p) {
			// The corner back/refresh button only acts on the already-focused
			// pane (it's hidden otherwise); a click on a just-focused pane
			// falls through to the pan/native-view path below.
			if prevFocus == p.ID && pointInPlus(r, sx, sy) {
				if a.urlStreams[p.ID] != nil {
					bridgeGoBack(p.ID)
				} else {
					// Frozen: go live (place the native view), same as
					// right-drag-down.
					gid := a.gridIDForPath(p.Path)
					if g, ok := a.c.Grid(gid); ok {
						if tile, ok := g.Tiles[p.TextFocus]; ok {
							a.openURLStream(p, tile.ID)
						}
					}
				}
				return nil
			}
			if !pointInPaneContent(r, sx, sy) {
				// Outer margin: ascent moved to the middle button / a
				// right-click on the corner circle. Swallow it.
				return nil
			}
			// Live pane: the native view owns content clicks; nothing to do.
			if a.urlStreams[p.ID] != nil {
				return nil
			}
			// Frozen pane: start a pan drag to navigate cover-mode overflow.
			a.urlPanDragging = true
			a.dragging = &dragState{
				originPaneID: p.ID,
				tileID:       "",
				startScreenX: sx,
				startScreenY: sy,
				curScreenX:   sx,
				curScreenY:   sy,
			}
			a.updateURLCursor(p, r)
			return nil
		}
		// The rendered/raw toggle is a DOM overlay button
		// (refreshFileToggle); its clicks never reach the canvas.
		// Anywhere outside the inner box (textarea / rendered area)
		// is "outer ring" — ascent. The textarea overlay catches
		// clicks inside in text mode; rendered mode falls through to
		// pan below.
		if !pointInFileInner(p, r, sx, sy) {
			// Outer ring: ascent moved to the middle button / a right-click
			// on the corner circle. Swallow it.
			return nil
		}
		// Rendered mode: clicks may land on a tile-embed (descent into the
		// referenced tile) or on plain content (start a pan drag). Text
		// mode: the textarea covers most of the pane and handles drag
		// itself. Margin clicks (text mode, narrow textarea) fall through
		// to a no-op.
		if p.TextMode == rpc.TextModeRendered {
			if hit := a.embedHitAt(p.ID, sx, sy); hit != nil {
				if a.descendIntoEmbed(p, hit) {
					return nil
				}
				// Embed under cursor but unresolvable (broken link or
				// cross-grid target): swallow the click so it doesn't start
				// a pan that would scroll the doc away from the embed.
				return nil
			}
			a.dragging = &dragState{
				originPaneID: p.ID,
				tileID:       "",
				startScreenX: sx,
				startScreenY: sy,
				curScreenX:   sx,
				curScreenY:   sy,
			}
		}
		return nil
	}

	// Click on the + button toggles the menu for this pane. The button is
	// only drawn on the focused pane, so it only acts when the pane was
	// already focused before this click; a click that merely focuses the
	// pane falls through to normal grid interaction (pan / palette).
	if prevFocus == p.ID && pointInPlus(r, sx, sy) {
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
	// Mousedown inside the palette: starting a template drag if it
	// landed on a tile, or swallowing the click (keeps the popover
	// open) if it landed in the gutter. Click outside the popover
	// dismisses it and falls through to normal interaction.
	if a.menuOpen && a.menuPaneID == p.ID {
		if pointInPalette(p, r, sx, sy) {
			idx := paletteTileIndexAt(p, r, sx, sy)
			if idx >= 0 {
				a.startTemplateDrag(p, r, idx, sx, sy)
				return nil
			}
			return nil
		}
		a.menuOpen = false
		a.draw()
		// fall through so the click also pans / selects
	}

	cellX, cellY := cellAtScreen(p, r, sx, sy)
	n := a.tileAtCell(p, cellX, cellY)
	parentCell := cellPx * p.Zoom
	ps := paneToDragdrop(p, r)
	a.dragging = &dragState{
		originPaneID: p.ID,
		tileID:       "",
		startScreenX: sx,
		startScreenY: sy,
		curScreenX:   sx,
		curScreenY:   sy,
		// Default source = the focused pane's leaf grid; overridden
		// below if we land on a child preview tile.
		srcGridID:   a.gridIDForPath(p.Path),
		srcPath:     slices.Clone(p.Path),
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
			a.dragging.originPaneRect = r
			a.dragging.srcGridID = n.ChildGridID
			a.dragging.srcPath = append(slices.Clone(p.Path), n.ID)
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
		a.dragging.originPaneRect = r
	}
	return nil
}

func (a *App) onMouseMove(this js.Value, args []js.Value) any {
	sx, sy := mouseXY(args[0], a.canvas)
	// Left-button pane resize takes precedence over everything else.
	if a.leftResize != nil {
		if args[0].Get("buttons").Int()&1 == 0 {
			// Left button released somewhere we didn't see — finish.
			a.leftResize = nil
			a.draw()
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
		if p, r, ok := a.paneAtScreen(sx, sy); ok && a.isURLDescent(p) {
			// Don't broadcast moves into the back-button area; the
			// page would see phantom cursor activity over an empty
			// region of its viewport.
			if pointInPlus(r, sx, sy) {
				a.canvas.Get("style").Set("cursor", "")
				return nil
			}
			if pointInPaneContent(r, sx, sy) {
				// Live pane: the native view owns the cursor (the canvas
				// won't get moves over it anyway). Frozen pane: grab cursor.
				if a.urlStreams[p.ID] != nil {
					a.canvas.Get("style").Set("cursor", "")
					return nil
				}
				// Frozen URL descent: show grab cursor.
				a.updateURLCursor(p, r)
				return nil
			}
			// Outside content area: restore default cursor.
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
	if a.menuOpen {
		p, r, ok := a.paneAtScreen(sx, sy)
		hover := -1
		if ok && p.ID == a.menuPaneID {
			hover = paletteTileIndexAt(p, r, sx, sy)
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
					// cover the predicate.)
					a.hiddenTileID = d.tileID
					a.hiddenPaneID = d.originPaneID
				}
			}
		} else {
			return nil
		}
	}
	if d.tileID == "" && !d.isTemplate {
		// Pan the source pane smoothly. For frozen URL descents, pan
		// translates the cover-mode crop (urlPanX/Y). In file-rendered
		// mode the drag scrolls the file's logical content; in grid mode
		// it pans the parent-grid view.
		focused := a.tree.FindPane(d.originPaneID)
		if focused != nil {
			if focused.TextFocus != "" && a.isURLDescent(focused) && a.urlStreams[focused.ID] == nil {
				// Frozen URL descent: translate cover-crop pan. The delta
				// is negated because dragging right should shift the image
				// right (show left portion), i.e., decrease panX.
				a.urlPanX[focused.ID] -= (sx - d.curScreenX)
				a.urlPanY[focused.ID] -= (sy - d.curScreenY)
				// Clamping is deferred to draw time (clampURLPan) because
				// we need the image natural dimensions which may vary frame
				// to frame as frames arrive.
			} else if focused.TextFocus != "" && focused.TextMode == rpc.TextModeRendered {
				z := nonzero(focused.TextZoom)
				focused.TextScrollX -= (sx - d.curScreenX) / z
				focused.TextScrollY -= (sy - d.curScreenY) / z
				if focused.TextScrollY < 0 {
					focused.TextScrollY = 0
				}
				if focused.TextScrollX < 0 {
					focused.TextScrollX = 0
				}
			} else {
				cellSize := cellPx * focused.Zoom
				focused.Cx -= (sx - d.curScreenX) / cellSize
				focused.Cy -= (sy - d.curScreenY) / cellSize
			}
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
	// Left-button release ends an in-flight pane-boundary resize. The ratio
	// was applied live during the move; nothing to commit, just clear.
	if a.leftResize != nil && args[0].Get("button").Int() == 0 {
		a.leftResize = nil
		a.draw()
		a.scheduleURLUpdate()
		return nil
	}
	// URL descent: the corner button and live content box are handled on
	// mousedown / by the native view; swallow the matching mouseup over them
	// so it doesn't leak into a gridwell gesture.
	sx, sy := mouseXY(args[0], a.canvas)
	if p, r, ok := a.paneAtScreen(sx, sy); ok && a.isURLDescent(p) {
		if pointInPlus(r, sx, sy) && args[0].Get("button").Int() == 0 {
			return nil
		}
		if a.urlStreams[p.ID] != nil && pointInPaneContent(r, sx, sy) && args[0].Get("button").Int() == 0 {
			return nil
		}
	}
	// End a frozen URL pan drag: clear the dragging flag and restore grab cursor.
	if a.urlPanDragging && args[0].Get("button").Int() == 0 {
		a.urlPanDragging = false
		if p, r, ok := a.paneAtScreen(sx, sy); ok && a.isURLDescent(p) {
			a.updateURLCursor(p, r)
		} else {
			a.canvas.Get("style").Set("cursor", "")
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

	// Snapshot every world-read the drop decision needs, ONCE, using the
	// local d (a.dragging is already nil above). DecideDrop then picks the
	// action and the switch executes side effects. onMouseMove builds the
	// same DropInput for the ghost preview, so preview and commit cannot
	// diverge — that divergence was the trashcan-delete regression.
	in := dragdrop.DropInput{
		Started:    d.started,
		IsTemplate: d.isTemplate,
		Clone:      d.clone, // always false here — clone commits via the right path above
		TileID:     d.tileID,
		OverDelete: a.overDeleteButton(d, sx, sy),
	}
	docTarget, overDoc := a.docDropTargetAt(sx, sy)
	in.OverDoc = overDoc
	in.DocReject = a.docRejectAt(sx, sy)
	t, haveT := a.dropTargetAt(sx, sy, d.tileID)
	in.HasTarget = haveT
	var dropX, dropY int64
	if haveT {
		in.Forbidden = a.dropForbiddenForMove(d, t)
		dropX, dropY = t.cellAtCursor(sx, sy, d.cellOffsetX, d.cellOffsetY)
		in.SameCell = t.gridID == d.srcGridID && dropX == d.snapshotTile.X && dropY == d.snapshotTile.Y
		in.Occupied = a.nodeAtCellInGrid(t.gridID, dropX, dropY) != nil
	}

	switch dragdrop.DecideDrop(in) {
	case dragdrop.DropNavigate:
		// Bare click (no movement): navigation.
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
			a.selectedTileID[focused.ID] = n.ID
		} else {
			delete(a.selectedTileID, focused.ID)
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
		// Pan drag end: just persist viewport state.
		a.scheduleURLUpdate()
		a.scheduleRootViewSave()
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

	case dragdrop.DropEmbed:
		// Dropping a tile onto a raw-mode text descent inserts a markdown
		// reference rather than moving the source. The doc isn't a placement
		// medium, so left-drag auto-promotes to "leave source" — same outcome
		// as right-drag, no tile orphaned.
		a.commitEmbedDrop(d, docTarget)
		a.cancelDragSnapBack(d)
		return nil

	case dragdrop.DropRejected:
		// Read-only doc, no target, forbidden cross-grid move, same cell, or
		// occupied — snap back without a doomed round-trip to the server.
		a.cancelDragSnapBack(d)
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

	srcPath := slices.Clone(d.srcPath)
	dstPath := slices.Clone(t.path)
	dstGridID := t.gridID
	srcGridID := d.srcGridID
	version := d.snapshotTile.Version

	// Left-drag is always a move; clone is handled by the right-drag path
	// (commitRightClone in right_button.go) and never reaches here.
	req := &rpc.MoveTileRequest{
		Path:       rpc.Path{WellIDs: srcPath},
		TileID:     d.tileID,
		Version:    version,
		DestGridID: dstGridID,
		DestPath:   rpc.Path{WellIDs: dstPath},
		X:          dropX,
		Y:          dropY,
	}
	a.postCrossGridMutate("MoveTile", srcGridID, dstGridID, func(ctx context.Context) (*rpc.Tile, error) {
		return a.cl.MoveTile(ctx, req)
	}, d)
	a.draw()
	return nil
}

// nodeAtCellInGrid returns the cached tile covering (cellX, cellY) in
// gridID, or nil. Mirrors tileAtCell but works against an arbitrary
// grid id rather than the focused pane's leaf grid.
func (a *App) nodeAtCellInGrid(gridID string, cellX, cellY int64) *rpc.Tile {
	g, ok := a.c.Grid(gridID)
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
// in-flight drag d: the + (trashcan) button of the pane the drag STARTED from.
//
// It takes the drag EXPLICITLY rather than reading a.dragging, because the
// commit path clears a.dragging before deciding what the drop means
// (onMouseUp / commitTileCenter both do `d := a.dragging; a.dragging = nil`).
// Reading the field here returned false at release — the tile fell through to a
// normal move and was placed under the trashcan instead of deleted — even
// though the live-drag preview looked correct. Keyed off the origin pane (not
// focus) so it works for both the left-drag move and the right-drag clone; a
// descended pane has no + button, so it's never a delete target.
func (a *App) overDeleteButton(d *dragState, sx, sy float64) bool {
	if d == nil || !d.started || d.tileID == "" {
		return false
	}
	p := a.tree.FindPane(d.originPaneID)
	if p == nil || p.TextFocus != "" {
		return false
	}
	return pointInPlus(paneRectFor(a, p), sx, sy)
}

// attemptDescentOrAscent routes a bare left-click (no drag) at (sx, sy)
// inside pane p to the right navigation gesture. Left-click only ever
// descends now; ascent is the middle button or a right-click on the
// corner circle (see ascendPane).
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
		// ascent moved to the middle button / a right-click on the corner
		// circle. Pan-drag (rendered mode) is handled in mousemove.
		return false
	}
	cellX, cellY := cellAtScreen(p, r, sx, sy)
	hit := a.tileAtCell(p, cellX, cellY)
	if hit == nil {
		return false
	}
	switch {
	case rpc.IsWellKind(hit.Kind):
		a.startDescent(p, hit)
		return true
	case rpc.IsContentDescentKind(hit.Kind):
		a.startFileDescent(p, hit, nil)
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

// canAscend reports whether pane p has somewhere to ascend to: it's
// descended into a text/url/shell tile (TextFocus) or into a child grid
// (non-empty Path). At the user's root neither holds.
func (a *App) canAscend(p *pane.Pane) bool {
	return p.TextFocus != "" || len(p.Path) > 0
}

// ascendPane performs the appropriate ascent for pane p: a file ascent
// when it's descended into a text/url/shell tile, a well ascent when
// it's in a child grid, nothing at the user's root. This is the single
// entry point for both ascent gestures (middle button, right-click on
// the corner circle).
func (a *App) ascendPane(p *pane.Pane) {
	if p.TextFocus != "" {
		a.startFileAscent(p)
	} else if len(p.Path) > 0 {
		a.startAscent(p)
	}
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
	var well rpc.Tile
	for ; level >= 0; level-- {
		parentPath := p.Path[:level]
		parentGridID := a.gridIDForPath(parentPath)
		g, ok := a.c.Grid(parentGridID)
		if !ok {
			a.fetchGrid(parentGridID)
			continue
		}
		w, ok := g.Tiles[p.Path[level]]
		if !ok {
			continue
		}
		well = w
		break
	}
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
		// No in-session saved state (e.g., user reloaded mid-descent
		// and is now ascending). Fall back to the bootstrap-supplied
		// root view when ascending all the way to root, so the user
		// returns to their preferred viewport rather than a forced
		// "well center, zoom 1" pose.
		if level == 0 && a.rootViewZoom > 0 {
			saved = &paneState{Cx: a.rootViewCx, Cy: a.rootViewCy, Zoom: a.rootViewZoom}
		}
		if saved == nil {
			saved = &paneState{Cx: switchTo.Cx, Cy: switchTo.Cy, Zoom: 1.0}
		}
	}

	// Distances in shared px-equivalent units so SplitN can apportion
	// time so each phase moves at a comparable visual speed.
	childDist := panDist(mid.Cx-from.Cx, mid.Cy-from.Cy, from.Zoom) +
		zoomDist(from.Zoom, mid.Zoom)
	parentDist := panDist(saved.Cx-switchTo.Cx, saved.Cy-switchTo.Cy, saved.Zoom) +
		zoomDist(switchTo.Zoom, saved.Zoom)
	durations := anim.SplitN([]float64{childDist, parentDist}, totalTransitionMs)

	// If the saved state carries a text descent (an embed click
	// originated the descent), restore it as the ascent landing.
	restoreFocus := saved.TextFocus
	restoreMode := saved.TextMode
	restoreScrollX := saved.TextScrollX
	restoreScrollY := saved.TextScrollY

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
		onComplete: func() {
			if restoreFocus == "" {
				return
			}
			fp := a.tree.FindPane(p.ID)
			if fp == nil {
				return
			}
			fp.TextFocus = restoreFocus
			fp.TextMode = restoreMode
			fp.TextScrollX = restoreScrollX
			fp.TextScrollY = restoreScrollY
			fp.TextZoom = fileFixedScale
			a.refreshFileOverlay()
		},
	})
}

// instantAscend is the fallback path when the parent grid isn't cached or
// the well row vanished. We just drop the last entry of the path; the user
// can wait for the parent to load and reposition manually.
func (a *App) instantAscend(p *pane.Pane, parentPath []string) {
	a.popPaneState(p.ID) // discard whatever was saved; we can't honor it.
	p.Path = parentPath
	p.Cx, p.Cy, p.Zoom = 0, 0, 1.0
	delete(a.selectedTileID, p.ID)
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
	a.pushPaneState(p.ID, paneState{Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom})

	r := paneRectFor(a, p)
	from := zoomtrans.Endpoints{
		Path: slices.Clone(p.Path),
		Cx:   p.Cx, Cy: p.Cy, Zoom: p.Zoom,
	}
	w := zoomtrans.Well{
		ID: well.ID, X: well.X, Y: well.Y, W: well.W, H: well.H,
		ViewX: well.ViewX, ViewY: well.ViewY, ViewZoom: well.ViewZoom,
	}
	mid, swap, final := zoomtrans.Descent(from, w, r.W, r.H, cellPx)
	a.fetchGrid(well.ChildGridID)

	parentDist := panDist(mid.Cx-from.Cx, mid.Cy-from.Cy, from.Zoom) +
		zoomDist(from.Zoom, mid.Zoom)
	childDist := zoomDist(swap.Zoom, final.Zoom)
	var durations []float64
	if childDist > 0 {
		durations = anim.SplitN([]float64{parentDist, childDist}, totalTransitionMs)
	} else {
		durations = []float64{totalTransitionMs, 0}
	}

	a.startTransition(&paneTransition{
		paneID: p.ID,
		segments: []transSegment{
			// A: parent pan+zoom toward well center at Overtake.
			{
				path:   from.Path,
				fromCx: from.Cx, fromCy: from.Cy, fromZoom: from.Zoom,
				toCx: mid.Cx, toCy: mid.Cy, toZoom: mid.Zoom,
				durationMs: durations[0],
			},
			// C: after the atomic path swap to (swap.Cx, swap.Cy, swap.Zoom),
			// ease the child zoom out to the stored saved-ratio. Same Cx,Cy
			// as swap (we landed where the saved view region is centered);
			// only the zoom moves. Zero-length when swap == final.
			{
				path:   swap.Path,
				fromCx: swap.Cx, fromCy: swap.Cy, fromZoom: swap.Zoom,
				toCx: final.Cx, toCy: final.Cy, toZoom: final.Zoom,
				durationMs: durations[1],
			},
		},
	})
}

// nonzero returns x or 1.0 if x is zero/negative. Saves a guard at every
// call site that divides by TextZoom.
func nonzero(x float64) float64 {
	if x <= 0 {
		return 1.0
	}
	return x
}

// startFileDescent zooms a pane into a text tile in a single
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
func (a *App) startFileDescent(p *pane.Pane, file *rpc.Tile, afterDescend func()) {
	a.pushPaneState(p.ID, paneState{Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom})

	r := paneRectFor(a, p)
	from := zoomtrans.Endpoints{
		Path: slices.Clone(p.Path),
		Cx:   p.Cx, Cy: p.Cy, Zoom: p.Zoom,
	}
	wellCx := float64(file.X) + float64(file.W)/2
	wellCy := float64(file.Y) + float64(file.H)/2
	target := fileOvertakeZoom(r, file.W, file.H)
	if target < from.Zoom {
		target = from.Zoom
	}

	// Eagerly fetch the blob so it's likely cached by the time the
	// transition lands. URL tiles don't have a blob; their preview
	// path goes through urlPreview instead.
	if file.Kind == rpc.KindText {
		a.fetchBlob(file.BlobID)
		// Source-backed text tiles (the @info tile in a proc-well, fs
		// file metadata) are reconciled server-side from live host
		// state. Trigger a parent GetGrid so the reconciler runs again
		// — the response (and the TileChanged event it fires) repoints
		// the tile at the freshest blob, which the next render frame
		// fetches automatically. The user briefly sees the previous
		// snapshot, then it snaps to current.
		if a.tileReadOnly(file) {
			a.fetchGrid(a.gridIDForPath(p.Path))
		}
	}

	fileID := file.ID
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
			fp.TextZoom = fileFixedScale
			// Reset URL pan state on each new descent — it's view state,
			// not tile state, so it does not survive across descents.
			delete(a.urlPanX, fp.ID)
			delete(a.urlPanY, fp.ID)
			a.refreshFileOverlay()
			// URL / shell descent show the frozen JPEG preview by
			// default. afterDescend fires here so an auto-go-live
			// path (fresh tile creation) can open the stream — the
			// explicit-callback shape keeps the auto-spawn decision
			// at the call site instead of mixing it into the descent.
			if afterDescend != nil {
				afterDescend()
			}
		},
	})
}

// startFileAscent reverses the text tile descent: animate zoom-out from the
// tile's footprint back to the saved viewport, then clear TextFocus and
// save the tile's content + scroll.
func (a *App) startFileAscent(p *pane.Pane) {
	if p.TextFocus == "" {
		return
	}
	gid := a.gridIDForPath(p.Path)
	g, ok := a.c.Grid(gid)
	if !ok {
		// Parent grid not cached — give up gracefully.
		a.exitFileFocusInstant(p)
		return
	}
	file, ok := g.Tiles[p.TextFocus]
	if !ok {
		a.exitFileFocusInstant(p)
		return
	}
	r := paneRectFor(a, p)
	wellCx := float64(file.X) + float64(file.W)/2
	wellCy := float64(file.Y) + float64(file.H)/2
	overtake := fileOvertakeZoom(r, file.W, file.H)
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
	a.saveFileBeforeAscent(p, file)

	// If we're ascending out of a URL tile, close the live stream (if any).
	if file.Kind == rpc.KindURL {
		a.closeURLStream(p.ID)
	}
	// Shell ascent: capture the JPEG, persist it as the frozen
	// preview, close the WS. closeShellStream handles all three.
	if file.Kind == rpc.KindShell {
		a.closeShellStream(p.ID)
	}

	// (Mode + framed window are persisted by saveFileBeforeAscent, which
	// also patches the cache so the preview is correct immediately.)

	// Reset parent-grid zoom to the overtake value so the animation
	// begins from "well filling the pane", regardless of how the user
	// zoomed within the text tile. Then clear TextFocus so the chrome (toggle
	// button, textarea) goes away as the animation begins.
	p.Zoom = overtake
	p.Cx, p.Cy = wellCx, wellCy
	p.TextFocus = ""
	a.refreshFileOverlay()

	// If the saved state had a TextFocus, the descent originated from
	// inside another text tile (an embed click) — restore that doc as
	// the ascent landing on transition complete.
	restoreFocus := saved.TextFocus
	restoreMode := saved.TextMode
	restoreScrollX := saved.TextScrollX
	restoreScrollY := saved.TextScrollY

	a.startTransition(&paneTransition{
		paneID: p.ID,
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
			if restoreFocus == "" {
				return
			}
			fp := a.tree.FindPane(p.ID)
			if fp == nil {
				return
			}
			fp.TextFocus = restoreFocus
			fp.TextMode = restoreMode
			fp.TextScrollX = restoreScrollX
			fp.TextScrollY = restoreScrollY
			fp.TextZoom = fileFixedScale
			a.refreshFileOverlay()
		},
	})
}

// exitFileFocusInstant is the fallback path when the parent grid isn't
// cached or the text tile row vanished while we were focused on it. We just
// clear TextFocus and reset the viewport to whatever was saved.
func (a *App) exitFileFocusInstant(p *pane.Pane) {
	a.closeURLStream(p.ID)   // no-op if not a URL descent
	a.closeShellStream(p.ID) // no-op if not a shell descent
	saved := a.popPaneState(p.ID)
	p.TextFocus = ""
	if saved != nil {
		p.Cx, p.Cy, p.Zoom = saved.Cx, saved.Cy, saved.Zoom
		// If the saved state captured a text descent (embed click), restore
		// it so a single ascent lands on the doc, not the grid behind it.
		if saved.TextFocus != "" {
			p.TextFocus = saved.TextFocus
			p.TextMode = saved.TextMode
			p.TextScrollX = saved.TextScrollX
			p.TextScrollY = saved.TextScrollY
			p.TextZoom = fileFixedScale
		}
	}
	a.refreshFileOverlay()
	a.draw()
	a.scheduleURLUpdate()
}

// saveWellViewBeforeAscent updates `well`'s ViewX/ViewY/ViewZoom so its
// parent-grid preview reflects the user's last position and zoom in
// the child grid. ViewZoom is stored as the intrinsic ratio
// childZoom_at_ascent / OvertakeZoom_at_ascent — window-independent so
// the preview stays stable across browser resizes. Mutates well in-place
// (so the local-side ascent transition uses the new values) and patches
// the cache so the parent's preview renders the new view immediately on
// path-swap. Posts SetWellView in a goroutine; the server's event will
// catch up the cache.
//
// No-op if the user's current center hasn't moved from the well's
// stored view (rounded to int cells), so casual ascents don't churn
// the DB.
func (a *App) saveWellViewBeforeAscent(p *pane.Pane, well *rpc.Tile, parentPath []string) {
	newViewX := int64(math.Round(p.Cx)) - well.W/2
	newViewY := int64(math.Round(p.Cy)) - well.H/2
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

	parentGridID := a.gridIDForPath(parentPath)
	req := &rpc.SetWellViewRequest{
		Path:     rpc.Path{WellIDs: slices.Clone(parentPath)},
		TileID:   well.ID,
		Version:  well.Version,
		ViewX:    newViewX,
		ViewY:    newViewY,
		ViewZoom: newViewZoom,
	}
	a.postPersist("SetWellView", parentGridID, func(ctx context.Context) (*rpc.Tile, error) {
		return a.cl.SetWellView(ctx, req)
	})
}

// saveFileBeforeAscent posts the editor buffer (if text mode is active)
// and the live scroll position back to the server. Failures are silently
// dropped; the user will see the local state on next descent and the
// server state otherwise.
func (a *App) saveFileBeforeAscent(p *pane.Pane, file rpc.Tile) {
	// SetTextView (and the framed-window cache patch) are text-tile
	// concerns — URL and shell tiles don't carry text_x/text_y/text_w
	// /text_h, and the server's SetTextView rejects non-text kinds with
	// InvalidArgument. Routing them through would surface as a 400 plus
	// a spurious "version conflict" refetch in the wasm dispatcher.
	if file.Kind != rpc.KindText {
		return
	}
	gid := a.gridIDForPath(p.Path)
	r := paneRectFor(a, p)
	scrollX := int64(p.TextScrollX + 0.5)
	scrollY := int64(p.TextScrollY + 0.5)

	// Capture the textarea contents (if any) before we tear it down.
	// Source-backed tiles are read-only — even if a stale TextMode said
	// "text" and the textarea had a buffer, we must not post it as the
	// new content (the server would reject it, and the local cache would
	// be transiently wrong).
	readOnly := a.tileReadOnly(&file)
	var buf string
	hasBuf := false
	if !readOnly && p.TextMode == rpc.TextModeText {
		ta := a.fileTextarea
		if !ta.IsNull() && !ta.IsUndefined() {
			buf = ta.Get("value").String()
			hasBuf = true
		}
	}
	// Pre-write the parent-grid preview to the user's edits before the ascent
	// transition. Tile-scoped (OptimisticEdit) so the optimistic content lands
	// only on this tile, not on any clone that shares its content-addressed blob.
	if hasBuf && file.BlobID != 0 {
		a.c.OptimisticEdit(gid, file.ID, []byte(buf))
	}

	// The framed window in doc px: scroll position + the inner box size
	// (= screen px, since scale is fixed at 1.0). The parent-grid preview
	// crops this rectangle out of the re-rendered doc.
	_, _, iw, ih := fileInnerBox(p, r)
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

	path := slices.Clone(p.Path)
	mode := p.TextMode
	go func() {
		curVersion := file.Version
		// Update content first if the user was editing.
		if hasBuf {
			tile, ok := a.postUpdateText(gid, &rpc.UpdateTextRequest{
				Path:    rpc.Path{WellIDs: path},
				TileID:  file.ID,
				Version: curVersion,
				Data:    []byte(buf),
			}, []byte(buf))
			if !ok {
				return
			}
			curVersion = tile.Version
		}
		// Persist the framed window + mode so re-descent and the preview
		// honor "however you left it" across reloads.
		req := &rpc.SetTextViewRequest{
			Path:     rpc.Path{WellIDs: path},
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
	}()
}

// startTemplateDrag arms a template drag from the i'th palette tile.
// The dragState is set up so the existing ghost machinery treats it
// like a regular tile drag (snapshot tile + cell offset), but with
// isTemplate=true so onMouseUp branches to creation instead of move.
// The palette stays open during the drag — it'll close on commit.
func (a *App) startTemplateDrag(p *pane.Pane, r pane.Rect, idx int, sx, sy float64) {
	if idx < 0 || idx >= len(templateKinds) {
		return
	}
	kind := templateKinds[idx]
	tx, ty, tw, _ := paletteTileRect(p, r, idx)
	a.dragging = &dragState{
		originPaneID:   p.ID,
		isTemplate:     true,
		template:       kind,
		startScreenX:   sx,
		startScreenY:   sy,
		curScreenX:     sx,
		curScreenY:     sy,
		cellOffsetX:    0.5,
		cellOffsetY:    0.5,
		snapshotTile:   templateGhostNode(kind),
		originScreenX:  tx,
		originScreenY:  ty,
		originPaneRect: r,
		// Start the ghost at the (fixed, zoom-independent) swatch size; the
		// drop-target machinery grows/shrinks it to the destination grid's
		// cell size as the cursor moves over a pane, and back to the swatch
		// size when off-grid — same lerp as dragging a tile across wells.
		srcCellSize: tw,
	}
}

// templateGhostNode synthesizes a 1×1 rpc.Tile matching the kind, so
// the ghost renderer can paint the in-flight tile using the same
// drawNode path that a real tile would use.
func templateGhostNode(kind templateKind) rpc.Tile {
	switch kind {
	case tplWell:
		return rpc.Tile{Kind: rpc.KindWell, W: 1, H: 1}
	case tplMarkdown:
		return rpc.Tile{Kind: rpc.KindText, W: 1, H: 1}
	case tplURL:
		return rpc.Tile{Kind: rpc.KindURL, W: 1, H: 1}
	case tplFileWell:
		return rpc.Tile{Kind: rpc.KindWell, W: 1, H: 1, AltText: rpc.AltFiles}
	case tplProcessWell:
		return rpc.Tile{Kind: rpc.KindWell, W: 1, H: 1, AltText: rpc.AltProcesses}
	case tplShell:
		return rpc.Tile{Kind: rpc.KindShell, W: 1, H: 1, AltText: "shell"}
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

	// URL still needs a URL up front; without one the tile is inert.
	// Every other template commits immediately with the snap-and-
	// create gesture wells use.
	if d.template == tplURL {
		a.ghost = nil
		a.draw()
		dp := destPane
		dx, dy := dropX, dropY
		a.openURLModal(
			func(url string) {
				a.createURLAtCell(dp, url, dx, dy)
				a.menuOpen = false
				a.draw()
			},
			func() {
				a.draw()
			},
		)
		return
	}

	// Wells + markdown commit immediately. Animate the ghost to the
	// snap target for a tactile landing. Markdown content is empty at
	// creation; the user descends and types.
	targetX, targetY := dpscreen.CellToScreen(float64(dropX), float64(dropY))
	if a.ghost != nil {
		a.ghost.paneID = destPane.ID
	}
	a.startSnap(targetX, targetY, snapMs)

	switch d.template {
	case tplWell:
		a.createWellAtCell(destPane, dropX, dropY)
	case tplMarkdown:
		a.createTextAtCell(destPane, []byte{}, dropX, dropY)
	case tplFileWell:
		a.createFileWellAtCell(destPane, "/", dropX, dropY)
	case tplProcessWell:
		a.createProcessWellAtCell(destPane, 1, dropX, dropY)
	case tplShell:
		a.createShellAtCell(destPane, dropX, dropY)
	}
	a.menuOpen = false
}

// createWellAtCell fires CreateWell at the given cell. Footprint is 1×1.
func (a *App) createWellAtCell(p *pane.Pane, cellX, cellY int64) {
	gid := a.gridIDForPath(p.Path)
	path := slices.Clone(p.Path)
	req := &rpc.CreateWellRequest{
		Path:   rpc.Path{WellIDs: path},
		GridID: gid, X: cellX, Y: cellY, W: 1, H: 1,
	}
	a.postTileMutate("CreateWell", gid, func(ctx context.Context) (*rpc.Tile, error) {
		return a.cl.CreateWell(ctx, req)
	}, nil)
}

// createTextAtCell fires CreateText at the given cell with the given
// initial bytes. Footprint is 1×1.
func (a *App) createTextAtCell(p *pane.Pane, data []byte, cellX, cellY int64) {
	gid := a.gridIDForPath(p.Path)
	path := slices.Clone(p.Path)
	req := &rpc.CreateTextRequest{
		Path:   rpc.Path{WellIDs: path},
		GridID: gid, X: cellX, Y: cellY, W: 1, H: 1,
		Data: data,
	}
	a.postTileMutate("CreateText", gid, func(ctx context.Context) (*rpc.Tile, error) {
		return a.cl.CreateText(ctx, req)
	}, nil)
}

// createURLAtCell fires CreateURL at the given cell with the given URL.
// Footprint is 1×1. After creation succeeds, the focused pane descends
// into the new tile and immediately goes live — typing a URL + Enter is
// an explicit "load this" gesture, so auto-live is the right behavior.
// This is the only auto-go-live path; all other descents into URL tiles
// start frozen and require an explicit refresh gesture.
func (a *App) createURLAtCell(p *pane.Pane, url string, cellX, cellY int64) {
	gid := a.gridIDForPath(p.Path)
	path := slices.Clone(p.Path)
	paneID := p.ID
	req := &rpc.CreateURLRequest{
		Path:   rpc.Path{WellIDs: path},
		GridID: gid, X: cellX, Y: cellY, W: 1, H: 1,
		URL: url,
	}
	a.postTileMutate("CreateURL", gid, func(ctx context.Context) (*rpc.Tile, error) {
		return a.cl.CreateURL(ctx, req)
	}, func(tile rpc.Tile) {
		// Auto-descend + auto-go-live: the user just typed a URL and
		// confirmed, which is an unambiguous "load this now" intent.
		// Find the focused pane and descend into the new tile, then
		// open the live stream once the transition completes.
		fp := a.tree.FindPane(paneID)
		if fp == nil || fp.TextFocus != "" {
			// Pane is gone or already descended — skip.
			return
		}
		a.startFileDescent(fp, &tile, func() {
			// afterDescend: open the URL stream so the pane goes live.
			ffp := a.tree.FindPane(paneID)
			if ffp == nil || ffp.TextFocus == "" {
				return
			}
			a.openURLStream(ffp, tile.ID)
		})
	})
}

// createFileWellAtCell fires CreateFileWell at the given cell, rooted at
// fsPath (canonical absolute). Palette default is "/" — the rest of the
// filesystem is reached by descending.
func (a *App) createFileWellAtCell(p *pane.Pane, fsPath string, cellX, cellY int64) {
	gid := a.gridIDForPath(p.Path)
	path := slices.Clone(p.Path)
	req := &rpc.CreateFileWellRequest{
		Path:   rpc.Path{WellIDs: path},
		GridID: gid, X: cellX, Y: cellY, W: 1, H: 1,
		FSPath: fsPath,
	}
	a.postTileMutate("CreateFileWell", gid, func(ctx context.Context) (*rpc.Tile, error) {
		return a.cl.CreateFileWell(ctx, req)
	}, nil)
}

// createProcessWellAtCell fires CreateProcessWell at the given cell,
// rooted at pid. Palette default is PID 1 (init).
func (a *App) createProcessWellAtCell(p *pane.Pane, pid int64, cellX, cellY int64) {
	gid := a.gridIDForPath(p.Path)
	path := slices.Clone(p.Path)
	req := &rpc.CreateProcessWellRequest{
		Path:   rpc.Path{WellIDs: path},
		GridID: gid, X: cellX, Y: cellY, W: 1, H: 1,
		PID: pid,
	}
	a.postTileMutate("CreateProcessWell", gid, func(ctx context.Context) (*rpc.Tile, error) {
		return a.cl.CreateProcessWell(ctx, req)
	}, nil)
}

// createShellAtCell fires CreateShell at the given cell, then
// auto-descends and auto-spawns the PTY — the user dropped a shell
// to use a shell, not to look at a placeholder. The first refresh
// creates the tile's gridwell-private tmux session; subsequent
// ascent / re-descent shows the frozen JPEG and refresh reattaches
// to the same tmux session (state preserved).
func (a *App) createShellAtCell(p *pane.Pane, cellX, cellY int64) {
	gid := a.gridIDForPath(p.Path)
	path := slices.Clone(p.Path)
	paneID := p.ID
	req := &rpc.CreateShellRequest{
		Path:   rpc.Path{WellIDs: path},
		GridID: gid, X: cellX, Y: cellY, W: 1, H: 1,
	}
	a.postTileMutate("CreateShell", gid, func(ctx context.Context) (*rpc.Tile, error) {
		return a.cl.CreateShell(ctx, req)
	}, func(tile rpc.Tile) {
		fp := a.tree.FindPane(paneID)
		if fp == nil || fp.TextFocus != "" {
			return
		}
		// Mirror createURLAtCell: descend, and once the transition
		// completes, open the PTY. afterDescend runs on the main loop
		// after onComplete sets TextFocus, so the new tile is in the
		// cache and the pane is in file-focus mode by the time
		// openShellStream looks for it.
		a.startFileDescent(fp, &tile, func() {
			ffp := a.tree.FindPane(paneID)
			if ffp == nil || ffp.TextFocus == "" {
				return
			}
			a.openShellStream(ffp, tile.ID)
		})
	})
}

// mouseXY returns the click coordinates relative to the canvas.
func mouseXY(ev js.Value, canvas js.Value) (float64, float64) {
	rect := canvas.Call("getBoundingClientRect")
	x := ev.Get("clientX").Float() - rect.Get("left").Float()
	y := ev.Get("clientY").Float() - rect.Get("top").Float()
	return x, y
}

// (Right-button gesture handling lives in right_button.go.)
