//go:build js && wasm

package main

import (
	"google.golang.org/protobuf/proto"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
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
	// Single-finger touch becomes the same mouse gestures; see touch.go.
	a.installTouchInput()
}

// onKeyDown owns the one window-level chord; overlays own every other key.
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

// armed reports the three gesture states to the verdict; see
// gesture.RecoverRelease.
func (a *App) armed() gesture.Armed {
	return gesture.Armed{
		LeftResize:  a.leftResize != nil,
		RightDrag:   a.rightDrag != nil,
		Drag:        a.dragging != nil,
		DragCreates: a.dragging != nil && a.dragging.intent.Creates(),
	}
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
func cellAtScreen(p *pane.Pane, r pane.Rect, sx, sy float64) (int64, int64) {
	return paneToDragdrop(p, r).CellAt(sx, sy)
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
	// A wheel over the bar zooms the focused pane from its center: the escape
	// hatch for a grid tiled wall to wall with wells. The band is below panes.
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
	// gesture.ClassifyWheel routes; this handler only resolves the impure facts.
	var hoverWell *gridwellv1.Tile
	wellCoverage := 0.0
	if p.ContentID() == "" {
		if t := a.tileAtScreen(p, r, sx, sy); t != nil && rpc.IsWellKind(t.Kind) && t.ChildGridId != "" {
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
		// A live URL view scrolls itself; a stray wheel must not zoom the pane.
		args[0].Call("preventDefault")
		return nil
	case gesture.WheelScrollDoc:
		// A text tile has a fixed scale, so the wheel scrolls the rendered
		// window. In text mode the textarea scrolls itself.
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
		// The wheel zooms the grid inside the hovered well, its stored preview
		// framing, not the grid the pane shows. The settle persister posts one
		// framing write per tile at flush.
		ps := paneToDragdrop(p, r)
		x0, y0 := ps.CellToScreen(float64(hoverWell.X), float64(hoverWell.Y))
		x1, _ := ps.CellToScreen(float64(hoverWell.X)+1, float64(hoverWell.Y))
		parentCell := x1 - x0
		wpx := float64(hoverWell.W) * parentCell
		hpx := float64(hoverWell.H) * parentCell
		zw := wellOf(hoverWell)
		// The float center accumulates across the burst: the first notch seeds
		// from the same EffectiveCenter the preview was drawn with, and later
		// notches feed the drift back in.
		cx0, cy0 := zoomtrans.EffectiveCenter(zw)
		if st, ok := a.persist.wellWheelPending[hoverWell.Id]; ok {
			cx0, cy0 = st.cx, st.cy
		}
		cx1, cy1, ratio, changed := zoomtrans.WellWheelView(dy, zw, parentCell,
			sx-(x0+wpx/2), sy-(y0+hpx/2), cx0, cy0, zoomFactor, wellZoomRatioMin, wellZoomRatioMax)
		if !changed {
			return nil
		}
		updated := proto.CloneOf(hoverWell)
		updated.ViewCx = cx1
		updated.ViewCy = cy1
		updated.ViewZoom = ratio
		a.c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{
			TileChanged: &gridwellv1.TileChanged{Tile: updated}}})
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

// wheelZoomPaneAt keeps the world point under (sx, sy) under it after the zoom,
// map-style. The bar-band wheel passes the pane's center.
func (a *App) wheelZoomPaneAt(p *pane.Pane, r pane.Rect, dy, sx, sy float64) {
	ps := paneToDragdrop(p, r)
	cellX, cellY := ps.ScreenToCell(sx, sy)
	p.Zoom, p.Cx, p.Cy = zoomtrans.WheelZoom(dy, p.Zoom, p.Cx, p.Cy, cellX, cellY, zoomFactor, zoomMin, zoomMax)
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
	// The tree and the viewport stay atomic across a transition.
	if a.trans.Any() {
		return nil
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
	prevFocus := a.tree.Focus
	a.focusToPane(p)
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

	// In a content descent every interactive surface owns its own clicks, so a
	// canvas left-click reaching here is chrome or margin and is swallowed.
	if p.ContentID() != "" {
		return nil
	}

	// A click inside the popover was claimed above, so this one is outside it.
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
		// Overridden below if the drag lands on a child preview tile.
		srcGridID:    a.gridIDForPane(p),
		srcCellSize:  parentCell,
		snapshotTile: &gridwellv1.Tile{},
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
			cellSize := cellPx * focused.Zoom
			focused.Cx -= (sx - d.curScreenX) / cellSize
			focused.Cy -= (sy - d.curScreenY) / cellSize
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
	if a.rightDrag != nil && args[0].Get("button").Int() == 2 {
		sx, sy := mouseXY(args[0], a.canvas)
		a.finishRightDrag(sx, sy)
		return nil
	}
	// The move applied the ratio live; the release decides the collapse.
	if a.leftResize != nil && args[0].Get("button").Int() == 0 {
		a.finishLeftResize()
		return nil
	}
	// A live view's content box is the native view's, so swallow a matching
	// mouseup over it. An armed gesture parks every live view, so a release that
	// ends one is never swallowed.
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
	cellX, cellY := cellAtScreen(p, r, sx, sy)
	hit := a.tileAtCell(p, cellX, cellY)
	if hit == nil {
		return false
	}
	switch gesture.DecideTileClick(gesture.ClickInput{
		Well:           rpc.IsWellKind(hit.Kind),
		ContentDescent: rpc.IsContentDescentKind(hit.Kind),
		Workspace:      rpc.IsWorkspaceKind(hit.Kind),
		URL:            hit.Kind == rpc.KindURL,
		URLEmpty:       hit.UrlString == "",
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
		a.descend(a.splitBelowForOpen(p), hit)
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
func (a *App) persistedGridView(p *pane.Pane, anchor string, path []string) (cx, cy, zoom float64, ok bool) {
	r := paneRectFor(a, p)
	if r.W <= 0 || r.H <= 0 {
		return 0, 0, 0, false
	}
	if len(path) == 0 {
		// The read side of persistFraming's root arm, the same 1x1 synthetic
		// doorway inverted. A root's framing rides its PluginInfo.
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
	w := wellOf(t)
	cx, cy, zoom = zoomtrans.StoredView(w, r.W, r.H, cellPx)
	return cx, cy, zoom, true
}

// splitBelowForOpen is the one programmatic split: a link opened out of a live
// tile and a ctrl-click descent land in the same place. A pane too short for
// two minimum panes returns p itself. The new pane sheds its inherited content
// level, which a live view cannot duplicate, unanimated: no ascent was asked.
func (a *App) splitBelowForOpen(p *pane.Pane) *pane.Pane {
	if !pane.CanSplit(pane.SideBottom, paneRectFor(a, p)) {
		return p
	}
	prev := a.tree.Focus
	newP, err := a.tree.SplitOnSideAt(pane.SideBottom, 0.5)
	if err != nil {
		return p
	}
	// SplitOnSideAt moves focus to the new pane, so the menu has to be told, or
	// it stays open on a pane that no longer has focus.
	a.menu.TransferFocus(prev, a.tree.Focus)
	if newP.ContentID() != "" {
		a.ascend(newP, 1, false)
	}
	return newP
}

func mouseXY(ev js.Value, canvas js.Value) (float64, float64) {
	rect := canvas.Call("getBoundingClientRect")
	x := ev.Get("clientX").Float() - rect.Get("left").Float()
	y := ev.Get("clientY").Float() - rect.Get("top").Float()
	return x, y
}
