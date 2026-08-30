//go:build js && wasm

package main

import (
	"math"
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/cache"
	"github.com/josephburnett/gridwell/client/gesture"
	"github.com/josephburnett/gridwell/client/pane"
)

// This file holds every in-flight gesture preview the right-button state
// machine paints — split, swap, tile-resize — plus their small canvas
// helpers. It is pure drawing off rightDragState; the classification and
// commit logic stay in right_button.go. The left-resize crush preview draws
// in drawLeftResizePreview.

// drawRightDragPreview paints the in-flight gesture's visual hint:
//   - Split: a horizontal or vertical line at the clamped cursor
//     projection. Blue when active — past the start in the expected
//     direction and within the valid range — grey otherwise.
//   - Swap: a double-headed arrow from origin pane center to either
//     the cursor or the destination pane center.
func (a *App) drawRightDragPreview() {
	rd := a.rightDrag
	if rd == nil {
		return
	}
	switch rd.kind {
	case rightDragSplit:
		a.drawPaneHotspotOverlay(rd)
		a.drawSplitPreview(rd)
	case rightDragSwap:
		a.drawPaneHotspotOverlay(rd)
		a.drawSwapPreview(rd)
	case rightDragTileCenter:
		a.drawTileHotspotOverlay(rd)
	case rightDragTileResize:
		a.drawTileHotspotOverlay(rd)
		a.drawTileResizePreview(rd)
	}
}

// drawPaneHotspotOverlay paints the affordance overlay for pane-management
// gestures (split / swap / resize). Mirrors drawTileHotspotOverlay for the
// pane level. Strictly grey and informational — the active gesture's own
// preview paints on top.
//
// Layout:
//   - Outer rectangle outline: the pane's inner content area (inset by paneBorderPx).
//   - Inner-third rectangle outline: the center 1/3 × 1/3 swap zone.
//   - Four cardinal split arrows, one per outer-edge band, pointing outward.
//   - Center glyph: swap arrows — the same for every pane, URL descents included.
func (a *App) drawPaneHotspotOverlay(rd *rightDragState) {
	// Resolve the pane and its rect based on the gesture kind.
	var paneID string
	switch rd.kind {
	case rightDragSplit:
		// The host follows the cursor: highlight the pane the split would
		// land in right now.
		if hp, _, ok := a.paneAtScreen(rd.curX, rd.curY); ok {
			paneID = hp.ID
		}
	case rightDragSwap:
		paneID = rd.originPaneID
	default:
		return
	}
	if paneID == "" {
		return
	}
	pr := a.paneRectByID(paneID)
	if pr.W <= 0 || pr.H <= 0 {
		return
	}

	inset := paneBorderPx
	r := pane.Rect{
		X: pr.X + inset,
		Y: pr.Y + inset,
		W: pr.W - 2*inset,
		H: pr.H - 2*inset,
	}
	if r.W <= 0 || r.H <= 0 {
		return
	}

	a.cctx.Set("strokeStyle", colorMuted)
	a.cctx.Set("lineWidth", 1.0)
	a.cctx.Set("lineCap", "round")
	a.cctx.Set("lineJoin", "round")

	// Outer rectangle outline.
	a.cctx.Call("strokeRect", r.X+0.5, r.Y+0.5, r.W-1, r.H-1)

	// Inner-third rectangle outline (the swap zone).
	innerX := r.X + r.W/3
	innerY := r.Y + r.H/3
	innerW := r.W / 3
	innerH := r.H / 3
	a.cctx.Call("strokeRect", innerX+0.5, innerY+0.5, innerW-1, innerH-1)

	// Four cardinal arrows, one in the middle of each outer-edge band,
	// pointing outward toward the edge (communicates "drag here to split").
	w := r.W
	h := r.H
	arrow := math.Min(w, h) * 0.10
	if arrow < 8 {
		arrow = 8
	}
	if arrow > 18 {
		arrow = 18
	}
	drawHotspotArrow(a.cctx, r.X+w/2, r.Y+h/6, 0, -arrow)  // top
	drawHotspotArrow(a.cctx, r.X+w/2, r.Y+h-h/6, 0, arrow) // bottom
	drawHotspotArrow(a.cctx, r.X+w/6, r.Y+h/2, -arrow, 0)  // left
	drawHotspotArrow(a.cctx, r.X+w-w/6, r.Y+h/2, arrow, 0) // right

	// Center glyph: swap, the same on every pane. A URL descent is not
	// special; go-live lives in the bar slot.
	cx := r.X + r.W/2
	cy := r.Y + r.H/2
	drawSwapGlyph(a.cctx, cx, cy, 16, colorMuted)

	a.cctx.Set("lineCap", "butt")
	a.cctx.Set("lineJoin", "miter")
}

// drawRefreshIcon draws a circular-arrow refresh icon centred at (cx, cy)
// with the given radius. The icon is strokes only: a single arc covering
// ~290° (leaving a gap at the top-right) with a small chevron arrowhead at
// the open end pointing in the direction of rotation (clockwise).
// Style: 2px line, round lineCap and lineJoin, matching drawURLBackButton.
func drawRefreshIcon(c js.Value, cx, cy, radius float64, color string) {
	c.Set("strokeStyle", color)
	c.Set("lineWidth", 2.0)
	c.Set("lineCap", "round")
	c.Set("lineJoin", "round")

	// Arc: starts at ~20° past top (top-right gap), sweeps clockwise 290°.
	// In canvas coords y points down, so clockwise is the positive direction.
	const gapDeg = 70.0                                 // degrees of gap left at the top-right
	startAngle := (-math.Pi/2 + (gapDeg/2)*math.Pi/180) // top + half-gap offset
	endAngle := startAngle + (360-gapDeg)*math.Pi/180

	c.Call("beginPath")
	c.Call("arc", cx, cy, radius, startAngle, endAngle, false)
	c.Call("stroke")

	// Chevron arrowhead at the open end (endAngle), pointing tangentially
	// in the clockwise (forward) direction.
	// Tangent direction at endAngle going clockwise: angle = endAngle + π/2.
	tipX := cx + math.Cos(endAngle)*radius
	tipY := cy + math.Sin(endAngle)*radius
	tangent := endAngle + math.Pi/2
	const headLen = 6.0
	const headAngle = 0.5 // ~29°
	c.Call("beginPath")
	c.Call("moveTo",
		tipX+math.Cos(tangent+math.Pi+headAngle)*headLen,
		tipY+math.Sin(tangent+math.Pi+headAngle)*headLen)
	c.Call("lineTo", tipX, tipY)
	c.Call("lineTo",
		tipX+math.Cos(tangent+math.Pi-headAngle)*headLen,
		tipY+math.Sin(tangent+math.Pi-headAngle)*headLen)
	c.Call("stroke")

	c.Set("lineWidth", 1.0)
	c.Set("lineCap", "butt")
	c.Set("lineJoin", "miter")
}

// drawTileHotspotOverlay paints the affordance overlay over the tile
// while a right-button gesture is in flight (or just primed). The
// overlay reads at a glance:
//   - Outer ring (everything outside the inner 1/3 × 1/3 of the tile)
//     is a single resize zone — grab anywhere out here, drag any
//     direction. Eight outward arrows (4 cardinal + 4 diagonal) make
//     "you can pull in any direction" explicit.
//   - Inner 1/3 × 1/3 square is the clone zone — marked with the
//     two-rectangles "clone" glyph.
//
// Strictly grey: informational, not interactive.
func (a *App) drawTileHotspotOverlay(rd *rightDragState) {
	left, top, w, h := tileScreenRect(&rd.tileNode, rd.tilePane, rd.tilePaneR)
	if w <= 0 || h <= 0 {
		return
	}
	tw := w / 3
	th := h / 3
	innerL := left + tw
	innerT := top + th

	a.cctx.Set("strokeStyle", colorMuted)
	a.cctx.Set("fillStyle", colorMuted)
	a.cctx.Set("lineWidth", 1.0)

	// Outer ring outline + inner-third square outline. No internal
	// 3×3 grid lines — the outer band is one continuous "grab-and-
	// drag" zone, not eight individual cells.
	a.cctx.Call("strokeRect", left+0.5, top+0.5, w-1, h-1)
	a.cctx.Call("strokeRect", innerL+0.5, innerT+0.5, tw-1, th-1)

	// Clone glyph: two overlapping rectangles inside the inner zone.
	ccx := left + w/2
	ccy := top + h/2
	gs := math.Min(tw, th) * 0.35
	if gs < 8 {
		gs = math.Min(tw, th) * 0.5
	}
	a.cctx.Call("strokeRect", ccx-gs/2, ccy-gs/2, gs, gs)
	a.cctx.Call("strokeRect", ccx-gs/2+gs*0.25, ccy-gs/2+gs*0.25, gs, gs)

	// Outward arrows in all eight compass directions. Each lives in
	// its own band/corner cell of the implicit 3×3 grid, pointing
	// straight out from the tile.
	arrow := math.Min(tw, th) * 0.28
	if arrow < 8 {
		arrow = 8
	}
	// Cardinal — center of each edge band.
	drawHotspotArrow(a.cctx, left+w/2, top+th/2, 0, -arrow)
	drawHotspotArrow(a.cctx, left+w/2, top+h-th/2, 0, arrow)
	drawHotspotArrow(a.cctx, left+tw/2, top+h/2, -arrow, 0)
	drawHotspotArrow(a.cctx, left+w-tw/2, top+h/2, arrow, 0)
	// Diagonals — center of each corner cell, 45° outward.
	d := arrow * 0.75
	drawHotspotArrow(a.cctx, left+tw/2, top+th/2, -d, -d)
	drawHotspotArrow(a.cctx, left+w-tw/2, top+th/2, d, -d)
	drawHotspotArrow(a.cctx, left+tw/2, top+h-th/2, -d, d)
	drawHotspotArrow(a.cctx, left+w-tw/2, top+h-th/2, d, d)
}

// drawHotspotArrow draws a simple line+head from (cx, cy) in direction
// (dx, dy). The head sits at the far end.
func drawHotspotArrow(c js.Value, cx, cy, dx, dy float64) {
	hx := cx + dx
	hy := cy + dy
	c.Call("beginPath")
	c.Call("moveTo", cx, cy)
	c.Call("lineTo", hx, hy)
	c.Call("stroke")
	// Arrow head: rotate ±2.5 rad off the direction.
	ang := math.Atan2(dy, dx)
	const headLen = 5.0
	c.Call("beginPath")
	c.Call("moveTo", hx, hy)
	c.Call("lineTo", hx+math.Cos(ang+2.5)*headLen, hy+math.Sin(ang+2.5)*headLen)
	c.Call("moveTo", hx, hy)
	c.Call("lineTo", hx+math.Cos(ang-2.5)*headLen, hy+math.Sin(ang-2.5)*headLen)
	c.Call("stroke")
}

// tileScreenRect returns the on-screen rectangle of tile n as drawn
// in pane p. Mirrors the math used by the parent-grid renderer.
func tileScreenRect(n *rpc.Tile, p *pane.Pane, r pane.Rect) (left, top, w, h float64) {
	ps := paneToDragdrop(p, r)
	left, top = ps.CellToScreen(float64(n.X), float64(n.Y))
	cellSize := cellPx * p.Zoom
	w = float64(n.W) * cellSize
	h = float64(n.H) * cellSize
	return
}

// drawTileResizePreview outlines the proposed new footprint in the
// pane's screen coordinates. The original tile keeps painting in
// place, so the preview is the new rectangle as a dashed blue stroke.
func (a *App) drawTileResizePreview(rd *rightDragState) {
	ps := paneToDragdrop(rd.tilePane, rd.tilePaneR)
	left, top := ps.CellToScreen(float64(rd.tileNewX), float64(rd.tileNewY))
	cellSize := cellPx * rd.tilePane.Zoom
	w := float64(rd.tileNewW) * cellSize
	h := float64(rd.tileNewH) * cellSize
	a.cctx.Set("strokeStyle", colorTileResize)
	a.cctx.Set("lineWidth", 2.0)
	a.cctx.Call("setLineDash", jsArray(6, 4))
	a.cctx.Call("strokeRect", left, top, w, h)
	a.cctx.Call("setLineDash", jsArray())
	a.cctx.Set("lineWidth", 1.0)
}

// jsArray makes a JS array from variadic float64 args. Used to set
// dash patterns on the canvas 2D context.
func jsArray(vals ...float64) js.Value {
	arr := make([]any, len(vals))
	for i, v := range vals {
		arr[i] = v
	}
	return js.ValueOf(arr)
}

// drawSplitPreview draws the partition line where the split would land right
// now: the side and host pane follow the drag, so the line lives in the pane
// under the cursor, flipping across the grabbed border as the cursor does.
// Blue when a release here would commit, grey while the drag is below the arm
// threshold or outside a valid position.
func (a *App) drawSplitPreview(rd *rightDragState) {
	host, r, ok := a.paneAtScreen(rd.curX, rd.curY)
	if !ok {
		return
	}
	a.drawSplitAxisHint(r, rd)
	side, armed := gesture.SplitSideFromDrag(rd.splitAxis, rd.startX, rd.startY, rd.curX, rd.curY)
	pos := rd.curX
	if rd.splitAxis == pane.Horizontal {
		pos = rd.curY
	}
	active := false
	if armed {
		var valid bool
		pos, valid = pane.SplitClampedPosition(side, r, rd.curX, rd.curY)
		active = valid
	}

	var g *cache.Grid
	gid := a.gridIDForPane(host)
	g, gridOK := a.c.Grid(gid)
	urlLive := a.urlViewFor(host.ID) != nil

	color := colorSplitInactive
	if active {
		color = a.paneBorderColorFor(host, g, gridOK, true /* focused */, urlLive)
	}
	a.cctx.Call("beginPath")
	if rd.splitAxis == pane.Horizontal {
		a.cctx.Call("moveTo", r.X, pos)
		a.cctx.Call("lineTo", r.X+r.W, pos)
	} else {
		a.cctx.Call("moveTo", pos, r.Y)
		a.cctx.Call("lineTo", pos, r.Y+r.H)
	}
	// Dark casing under the line so it stays visible against the grey
	// markdown-preview background, where the inactive grey line would
	// otherwise blend in. Same path, stroked twice.
	a.cctx.Set("strokeStyle", "rgba(0,0,0,0.55)")
	a.cctx.Set("lineWidth", 4.5)
	a.cctx.Call("stroke")
	a.cctx.Set("strokeStyle", color)
	a.cctx.Set("lineWidth", 2.0)
	a.cctx.Call("stroke")
	a.cctx.Set("lineWidth", 1.0)
}

// drawSplitAxisHint paints the gesture identity at the grab point: two
// opposing arrows along the split's axis, meaning "drag either way to open a
// new pane on that side".
func (a *App) drawSplitAxisHint(r pane.Rect, rd *rightDragState) {
	a.cctx.Set("strokeStyle", colorMuted)
	a.cctx.Set("lineWidth", 1.0)
	arm := math.Min(r.W, r.H) * 0.12
	if rd.splitAxis == pane.Horizontal {
		drawHotspotArrow(a.cctx, rd.startX, rd.startY-8, 0, -arm)
		drawHotspotArrow(a.cctx, rd.startX, rd.startY+8, 0, arm)
	} else {
		drawHotspotArrow(a.cctx, rd.startX-8, rd.startY, -arm, 0)
		drawHotspotArrow(a.cctx, rd.startX+8, rd.startY, arm, 0)
	}
}

// drawSwapPreview draws the swap affordance overlay. Before any drag
// motion (or while still inside the origin pane), an inline "swap"
// glyph sits at the cursor as a hint: "this is a swap gesture; drag
// to another pane." Once the cursor lands on a different pane, that
// hint upgrades to a full double-headed arrow snapping to the
// destination pane center.
func (a *App) drawSwapPreview(rd *rightDragState) {
	originPane := a.tree.FindPane(rd.originPaneID)
	if originPane == nil {
		return
	}
	originRect := paneRectFor(a, originPane)
	x1 := originRect.X + originRect.W/2
	y1 := originRect.Y + originRect.H/2

	// Highlight the origin pane interior so the user sees what's
	// being moved. Faint to keep the pane content readable.
	a.cctx.Set("strokeStyle", colorMuted)
	a.cctx.Set("lineWidth", 1.0)
	a.cctx.Call("setLineDash", jsArray(4, 4))
	a.cctx.Call("strokeRect",
		originRect.X+resizeBandPx+0.5, originRect.Y+resizeBandPx+0.5,
		originRect.W-2*resizeBandPx-1, originRect.H-2*resizeBandPx-1)
	a.cctx.Call("setLineDash", jsArray())

	destPane, destRect, ok := a.paneAtScreen(rd.curX, rd.curY)
	activeTarget := ok && destPane.ID != rd.originPaneID
	if !activeTarget {
		// No destination yet — paint just the swap glyph at the
		// cursor so the user sees the gesture identity.
		drawSwapGlyph(a.cctx, rd.curX, rd.curY, 18, colorMuted)
		return
	}
	x2 := destRect.X + destRect.W/2
	y2 := destRect.Y + destRect.H/2
	a.cctx.Set("strokeStyle", colorSwapArrow)
	a.cctx.Set("fillStyle", colorSwapArrow)
	a.cctx.Set("lineWidth", 2.0)
	a.cctx.Call("beginPath")
	a.cctx.Call("moveTo", x1, y1)
	a.cctx.Call("lineTo", x2, y2)
	a.cctx.Call("stroke")
	a.cctx.Set("lineWidth", 1.0)
	angle := math.Atan2(y2-y1, x2-x1)
	const arrowLen = 12.0
	drawArrowHead(a, x1, y1, angle+math.Pi, arrowLen)
	drawArrowHead(a, x2, y2, angle, arrowLen)
}

// drawSwapGlyph paints a compact double-headed horizontal arrow ⇄
// centered at (cx, cy) in the given color.
func drawSwapGlyph(c js.Value, cx, cy, size float64, color string) {
	c.Set("strokeStyle", color)
	c.Set("lineWidth", 1.5)
	gap := size * 0.3
	// Top arrow points right; bottom arrow points left.
	yTop := cy - gap/2
	yBot := cy + gap/2
	c.Call("beginPath")
	c.Call("moveTo", cx-size/2, yTop)
	c.Call("lineTo", cx+size/2, yTop)
	c.Call("moveTo", cx+size/2-size*0.25, yTop-size*0.2)
	c.Call("lineTo", cx+size/2, yTop)
	c.Call("lineTo", cx+size/2-size*0.25, yTop+size*0.2)
	c.Call("moveTo", cx-size/2, yBot)
	c.Call("lineTo", cx+size/2, yBot)
	c.Call("moveTo", cx-size/2+size*0.25, yBot-size*0.2)
	c.Call("lineTo", cx-size/2, yBot)
	c.Call("lineTo", cx-size/2+size*0.25, yBot+size*0.2)
	c.Call("stroke")
	c.Set("lineWidth", 1.0)
}

// drawArrowHead paints a small filled triangle at (cx, cy) pointing
// in the direction `angle` (radians). Used by the swap preview.
func drawArrowHead(a *App, cx, cy, angle, size float64) {
	tipX := cx + math.Cos(angle)*size
	tipY := cy + math.Sin(angle)*size
	leftX := cx + math.Cos(angle+2.5)*size
	leftY := cy + math.Sin(angle+2.5)*size
	rightX := cx + math.Cos(angle-2.5)*size
	rightY := cy + math.Sin(angle-2.5)*size
	a.cctx.Call("beginPath")
	a.cctx.Call("moveTo", tipX, tipY)
	a.cctx.Call("lineTo", leftX, leftY)
	a.cctx.Call("lineTo", rightX, rightY)
	a.cctx.Call("closePath")
	a.cctx.Call("fill")
}

// drawLeftResizePreview paints the left resize affordance; the left drag owns
// resize and close. Two layers:
//   - Always: highlight the divider being dragged in grey with an
//     orthogonal double-headed arrow.
//   - Every corridor segment the drag has pressed past its bump gets a red
//     border: a release closes all of them, and backing off un-reds them one
//     by one.
func (a *App) drawLeftResizePreview(lr *leftResizeState) {
	// Live geometry, every frame: the cascade moves ancestor ratios, so the
	// grabbed split's container and its boundary are wherever the applied
	// layout says they are. An arm-time copy goes stale mid-drag and closes
	// panes on a legal mid-corridor release.
	root := a.tree.Root
	rootRect := a.rootLayoutRect()
	r, ok := pane.LocateSplit(root, rootRect, lr.targetSplit)
	if !ok {
		return
	}
	aRect, _ := pane.SplitRect(r, lr.splitDir, lr.targetSplit.Ratio)
	// Divider hint: a thin grey band along the shared edge between
	// aRect and bRect, plus a double-headed arrow centered on it.
	a.cctx.Set("strokeStyle", colorMuted)
	a.cctx.Set("lineWidth", 2.0)
	a.cctx.Call("setLineDash", jsArray(4, 4))
	a.cctx.Call("beginPath")
	if lr.splitDir == pane.Horizontal {
		dy := aRect.Y + aRect.H
		a.cctx.Call("moveTo", r.X, dy)
		a.cctx.Call("lineTo", r.X+r.W, dy)
		a.cctx.Call("stroke")
		a.cctx.Call("setLineDash", jsArray())
		cx := r.X + r.W/2
		drawHotspotArrow(a.cctx, cx, dy-12, 0, -10)
		drawHotspotArrow(a.cctx, cx, dy+12, 0, 10)
	} else {
		dx := aRect.X + aRect.W
		a.cctx.Call("moveTo", dx, r.Y)
		a.cctx.Call("lineTo", dx, r.Y+r.H)
		a.cctx.Call("stroke")
		a.cctx.Call("setLineDash", jsArray())
		cy := r.Y + r.H/2
		drawHotspotArrow(a.cctx, dx-12, cy, -10, 0)
		drawHotspotArrow(a.cctx, dx+12, cy, 10, 0)
	}
	a.cctx.Set("lineWidth", 1.0)

	// The crush verdict: every corridor segment the drag has pressed past
	// its live bump reds, and the release reads the identical stored
	// lr.crush.Red() state, so the red set and the closed set cannot
	// diverge. Rects are live (SegmentRects), tracking the crush.
	red := lr.crush.Red()
	if len(red) == 0 {
		return
	}
	a.cctx.Set("strokeStyle", colorCloseWarn)
	a.cctx.Set("lineWidth", paneBorderPx)
	half := paneBorderPx / 2
	for _, rr := range pane.SegmentRects(root, rootRect, lr.targetSplit, red) {
		a.cctx.Call("strokeRect", rr.X+half, rr.Y+half, rr.W-paneBorderPx, rr.H-paneBorderPx)
	}
	a.cctx.Set("lineWidth", 1.0)
}

// drawGhostNoEntryBadge paints the international "no entry" sign (red
// disc, white ring, white diagonal slash) centered at (cx, cy). Used as
// a ghost overlay during drags whose drop would be rejected — most
// notably left-drag (move) of a source-grid tile into a regular grid,
// which the server rejects in favor of right-drag (clone/link).
func drawGhostNoEntryBadge(c js.Value, cx, cy, size float64) {
	radius := size * 0.32
	if radius < 14 {
		radius = 14
	}
	ringW := radius * 0.18
	// Red disc.
	c.Set("fillStyle", colorNoEntryFill)
	c.Call("beginPath")
	c.Call("arc", cx, cy, radius, 0.0, 2*math.Pi, false)
	c.Call("fill")
	// White ring inside the red.
	c.Set("strokeStyle", colorNoEntryStroke)
	c.Set("lineWidth", ringW)
	c.Call("beginPath")
	c.Call("arc", cx, cy, radius-ringW/2-1, 0.0, 2*math.Pi, false)
	c.Call("stroke")
	// White diagonal slash (top-left → bottom-right).
	slashR := radius - ringW*1.4
	angle := math.Pi / 4
	c.Set("lineCap", "round")
	c.Call("beginPath")
	c.Call("moveTo", cx+math.Cos(angle+math.Pi)*slashR, cy+math.Sin(angle+math.Pi)*slashR)
	c.Call("lineTo", cx+math.Cos(angle)*slashR, cy+math.Sin(angle)*slashR)
	c.Call("stroke")
	c.Set("lineCap", "butt")
	c.Set("lineWidth", 1.0)
}

// drawGhostLinkBadge paints the chain-link glyph over the dragged ghost when
// the drop would create a cross-plugin link: the in-flight ghost teaches that
// a left-drag links, never copies.
func drawGhostLinkBadge(c js.Value, cx, cy, size float64) {
	stroke := size * 0.10
	if stroke < 2 {
		stroke = 2
	}
	r := size * 0.20
	off := r * 0.55
	c.Set("strokeStyle", colorPlusFg)
	c.Set("lineWidth", stroke)
	c.Call("beginPath")
	c.Call("arc", cx-off, cy, r, 0.0, 2*math.Pi, false)
	c.Call("stroke")
	c.Call("beginPath")
	c.Call("arc", cx+off, cy, r, 0.0, 2*math.Pi, false)
	c.Call("stroke")
}
