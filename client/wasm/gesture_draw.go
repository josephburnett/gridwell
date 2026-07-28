//go:build js && wasm

package main

import (
	"math"
	"syscall/js"

	"github.com/josephburnett/gridwell/client/cache"
	"github.com/josephburnett/gridwell/client/gesture"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// This file holds every in-flight gesture preview the right-button state
// machine paints (split / swap / resize / tile-resize / ascend / URL-refresh
// hints) plus their small canvas helpers. Pure drawing off rightDragState —
// the classification and commit logic stay in right_button.go.

// drawRightDragPreview paints the in-flight gesture's visual hint:
//   - Split: a horizontal/vertical line at the clamped cursor projection.
//     Blue when "active" (past start in expected direction AND in
//     valid range), grey otherwise.
//   - Swap: a double-headed arrow from origin pane center to either
//     the cursor or the destination pane center.
//   - Resize: a red border on the side that would close on release,
//     so the user can drag back before letting go.
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
	case rightDragEmbedHint:
		a.drawEmbedHintOverlay(rd)
	case rightDragAscend:
		a.drawAscendPreview(rd)
	}
}

// drawAscendPreview paints the ascent hint over the corner circle while a
// right-click-to-ascend gesture is in flight: a highlighted circle with
// an upward chevron when armed (cursor still inside the circle), dimmed
// grey when the cursor has dragged out and release would cancel.
func (a *App) drawAscendPreview(rd *rightDragState) {
	p := a.tree.FindPane(rd.ascendPaneID)
	pr := a.paneRectByID(rd.ascendPaneID)
	if p == nil || pr.W <= 0 || pr.H <= 0 {
		return
	}
	c := a.cctx
	cx, cy := plusButtonCenter(p, pr)
	rad := float64(plusButtonRadius)

	fill := colorSwapArrow // blue == armed (same "active gesture" blue)
	if !rd.cursorInCircle {
		fill = colorSplitInactive // grey == release-to-cancel
	}
	c.Set("fillStyle", fill)
	c.Call("beginPath")
	c.Call("arc", cx, cy, rad, 0, 2*math.Pi)
	c.Call("fill")

	// Upward chevron == ascend.
	c.Set("strokeStyle", "#ffffff")
	c.Set("lineWidth", 2.5)
	c.Set("lineCap", "round")
	c.Call("beginPath")
	c.Call("moveTo", cx-7, cy+4)
	c.Call("lineTo", cx, cy-5)
	c.Call("lineTo", cx+7, cy+4)
	c.Call("stroke")
	c.Set("lineCap", "butt")
	c.Set("lineWidth", 1.0)
}

// drawEmbedHintOverlay paints the chain-link glyph centered inside the
// rendered embed under the cursor. Surfaces the message that this is a
// hyperlink — nothing else right-click can do here.
func (a *App) drawEmbedHintOverlay(rd *rightDragState) {
	c := a.cctx
	x, y, w, h := rd.embedRect[0], rd.embedRect[1], rd.embedRect[2], rd.embedRect[3]
	// Dim the embed slightly so the badge reads.
	c.Set("fillStyle", "rgba(0,0,0,0.35)")
	c.Call("fillRect", x, y, w, h)
	drawGhostLinkBadge(c, x+w/2, y+h/2, min(w, h))
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
		paneID = rd.splitPaneID
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

	// Center glyph: swap, the same on every pane (a URL descent is no
	// longer special — go-live lives on the corner circle).
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

// drawSplitPreview draws the partition line and a grey "split zone"
// hint behind it so the user sees the gesture identity immediately on
// right-button-down — before any drag motion. The line renders blue
// when a release here would commit (past start, in valid range) and
// grey otherwise.
func (a *App) drawSplitPreview(rd *rightDragState) {
	a.drawSplitZoneHint(rd)
	pos, valid := pane.SplitClampedPosition(rd.splitSide, rd.splitPane, rd.curX, rd.curY)
	active := valid && pane.SplitGestureActive(rd.splitSide, rd.startX, rd.startY, rd.curX, rd.curY)

	// Find the pane being split for color resolution.
	p := a.tree.FindPane(rd.splitPaneID)
	var g *cache.Grid
	gridOK := false
	if p != nil {
		gid := a.gridIDForPane(p)
		g, gridOK = a.c.Grid(gid)
	}
	urlLive := false
	if p != nil {
		urlLive = a.urlViewFor(p.ID) != nil
	}

	color := colorSplitInactive
	if active {
		color = a.paneBorderColorFor(p, g, gridOK, true /* focused */, urlLive)
	}
	a.cctx.Call("beginPath")
	r := rd.splitPane
	switch rd.splitSide {
	case pane.SideTop, pane.SideBottom:
		// Horizontal divider line at y=pos, full pane width.
		a.cctx.Call("moveTo", r.X, pos)
		a.cctx.Call("lineTo", r.X+r.W, pos)
	case pane.SideLeft, pane.SideRight:
		// Vertical divider at x=pos, full pane height.
		a.cctx.Call("moveTo", pos, r.Y)
		a.cctx.Call("lineTo", pos, r.Y+r.H)
	}
	// Dark casing under the line so it stays visible against the lightened
	// split-zone hint and the grey markdown-preview background, where the
	// inactive grey line would otherwise blend in. Same path, stroked twice.
	a.cctx.Set("strokeStyle", "rgba(0,0,0,0.55)")
	a.cctx.Set("lineWidth", 4.5)
	a.cctx.Call("stroke")
	a.cctx.Set("strokeStyle", color)
	a.cctx.Set("lineWidth", 2.0)
	a.cctx.Call("stroke")
	a.cctx.Set("lineWidth", 1.0)
}

// drawSplitZoneHint paints the trapezoidal split sector for the side
// the gesture was armed on (top/bottom/left/right) plus a "drag away
// from the edge to split" hint arrow. Faint grey so the underlying
// pane content stays legible.
func (a *App) drawSplitZoneHint(rd *rightDragState) {
	r := rd.splitPane
	tl := pointXY{r.X, r.Y}
	tr := pointXY{r.X + r.W, r.Y}
	bl := pointXY{r.X, r.Y + r.H}
	br := pointXY{r.X + r.W, r.Y + r.H}
	cx := r.X + r.W/2
	cy := r.Y + r.H/2
	var poly []pointXY
	var arrowX, arrowY, dx, dy float64
	switch rd.splitSide {
	case pane.SideTop:
		poly = []pointXY{tl, tr, {cx, cy}}
		arrowX, arrowY = cx, r.Y+r.H*0.18
		dx, dy = 0, r.H*0.18
	case pane.SideBottom:
		poly = []pointXY{bl, br, {cx, cy}}
		arrowX, arrowY = cx, r.Y+r.H*0.82
		dx, dy = 0, -r.H*0.18
	case pane.SideLeft:
		poly = []pointXY{tl, bl, {cx, cy}}
		arrowX, arrowY = r.X+r.W*0.18, cy
		dx, dy = r.W*0.18, 0
	case pane.SideRight:
		poly = []pointXY{tr, br, {cx, cy}}
		arrowX, arrowY = r.X+r.W*0.82, cy
		dx, dy = -r.W*0.18, 0
	default:
		return
	}
	a.cctx.Set("fillStyle", colorPlusBg)
	a.cctx.Set("globalAlpha", 0.45)
	a.cctx.Call("beginPath")
	a.cctx.Call("moveTo", poly[0].x, poly[0].y)
	for _, p := range poly[1:] {
		a.cctx.Call("lineTo", p.x, p.y)
	}
	a.cctx.Call("closePath")
	a.cctx.Call("fill")
	a.cctx.Set("globalAlpha", 1.0)
	a.cctx.Set("strokeStyle", colorMuted)
	a.cctx.Set("lineWidth", 1.0)
	drawHotspotArrow(a.cctx, arrowX, arrowY, dx, dy)
}

type pointXY struct{ x, y float64 }

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

// drawLeftResizePreview paints the LEFT resize affordance (issue #203: the
// left drag owns resize AND close). Two layers:
//   - Always: highlight the divider being dragged in grey with an
//     orthogonal double-headed arrow.
//   - When releasing HERE would collapse a side (crushed past the minimum
//     wall): paint a red border around that side, so the user knows
//     they're about to close it (and can drag back before releasing).
func (a *App) drawLeftResizePreview(lr *leftResizeState) {
	// LIVE geometry, every frame: the cascade moves ancestor ratios, so the
	// grabbed split's container and its boundary are wherever the applied
	// layout says they are — an arm-time copy goes stale mid-drag (the
	// stale-container bug closed panes on a legal mid-corridor release).
	root := a.tree.Root
	rootRect := a.rootLayoutRect()
	r, ok := pane.LocateSplit(root, rootRect, lr.targetSplit)
	if !ok {
		return
	}
	// The collapse verdict comes from the SAME corridor edges the release
	// reads (pane.CorridorSpan, issue #204) and the SAME cursor the move
	// last applied — finishLeftResize reads the identical inputs, so the
	// red "about to close" border can never mark a side the release won't
	// drop, and it only lights once the cursor has traveled all the way
	// across to the corridor's edge.
	corStart, corEnd, ok := pane.CorridorSpan(root, rootRect, lr.targetSplit)
	if !ok {
		return
	}
	collapse := gesture.ResizeOutcome(lr.splitDir, lr.curX, lr.curY, corStart, corEnd)
	aRect, bRect := pane.SplitRect(r, lr.splitDir, lr.targetSplit.Ratio)
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

	if collapse == gesture.CollapseNone {
		return
	}
	target := bRect
	if collapse == gesture.CollapseA {
		target = aRect
	}
	a.cctx.Set("strokeStyle", colorCloseWarn)
	a.cctx.Set("lineWidth", paneBorderPx)
	half := paneBorderPx / 2
	a.cctx.Call("strokeRect", target.X+half, target.Y+half, target.W-paneBorderPx, target.H-paneBorderPx)
	a.cctx.Set("lineWidth", 1.0)
}
