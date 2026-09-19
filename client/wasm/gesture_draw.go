//go:build js && wasm

package main

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"math"
	"syscall/js"

	"github.com/josephburnett/gridwell/client/cache"
	"github.com/josephburnett/gridwell/client/gesture"
	"github.com/josephburnett/gridwell/client/pane"
)

// Every in-flight gesture preview the right-button state machine paints, plus
// their canvas helpers. Pure drawing off rightDragState; classification and
// commit stay in right_button.go.

// drawRightDragPreview paints the in-flight gesture's visual hint.
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

// drawPaneHotspotOverlay is drawTileHotspotOverlay at the pane level: an
// outer rectangle, an inner third for the swap zone, four split arrows and
// the swap glyph. Strictly grey, since the active gesture's own preview
// paints on top.
func (a *App) drawPaneHotspotOverlay(rd *rightDragState) {
	var paneID string
	switch rd.kind {
	case rightDragSplit:
		// The host follows the cursor.
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

	a.cctx.Set("strokeStyle", a.pal.Muted)
	a.cctx.Set("lineWidth", 1.0)
	a.cctx.Set("lineCap", "round")
	a.cctx.Set("lineJoin", "round")

	a.cctx.Call("strokeRect", r.X+0.5, r.Y+0.5, r.W-1, r.H-1)

	innerX := r.X + r.W/3
	innerY := r.Y + r.H/3
	innerW := r.W / 3
	innerH := r.H / 3
	a.cctx.Call("strokeRect", innerX+0.5, innerY+0.5, innerW-1, innerH-1)

	// Outward, so the arrows read as "drag here to split".
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

	// The swap glyph is the same on every pane: a URL descent is not
	// special, since go-live lives in the bar slot.
	cx := r.X + r.W/2
	cy := r.Y + r.H/2
	drawSwapGlyph(a.cctx, cx, cy, 16, a.pal.Muted)

	endGlyph(a.cctx)
}

// drawRefreshIcon draws a circular-arrow refresh icon: one arc with a gap at
// the top-right and a chevron arrowhead at the open end.
func drawRefreshIcon(c js.Value, cx, cy, radius float64, color string) {
	beginSlotGlyph(c, color)

	// Canvas y points down, so clockwise is the positive direction.
	const gapDeg = 70.0
	startAngle := (-math.Pi/2 + (gapDeg/2)*math.Pi/180)
	endAngle := startAngle + (360-gapDeg)*math.Pi/180

	c.Call("beginPath")
	c.Call("arc", cx, cy, radius, startAngle, endAngle, false)
	c.Call("stroke")

	// The arrowhead points tangentially forward, so the tangent at endAngle
	// is endAngle + pi/2.
	tipX := cx + math.Cos(endAngle)*radius
	tipY := cy + math.Sin(endAngle)*radius
	tangent := endAngle + math.Pi/2
	const headLen = 6.0
	const headAngle = 0.5
	c.Call("beginPath")
	c.Call("moveTo",
		tipX+math.Cos(tangent+math.Pi+headAngle)*headLen,
		tipY+math.Sin(tangent+math.Pi+headAngle)*headLen)
	c.Call("lineTo", tipX, tipY)
	c.Call("lineTo",
		tipX+math.Cos(tangent+math.Pi-headAngle)*headLen,
		tipY+math.Sin(tangent+math.Pi-headAngle)*headLen)
	c.Call("stroke")

	endGlyph(c)
}

// drawTileHotspotOverlay paints the tile's affordances while a right-button
// gesture is in flight or primed: the outer ring is one resize zone, the
// inner third the copy and link zone. The copy glyph does not change with
// ctrl, because the ghost is what says which mode is armed.
func (a *App) drawTileHotspotOverlay(rd *rightDragState) {
	left, top, w, h := tileScreenRect(rd.tileNode, rd.tilePane, rd.tilePaneR)
	if w <= 0 || h <= 0 {
		return
	}
	tw := w / 3
	th := h / 3
	innerL := left + tw
	innerT := top + th

	a.cctx.Set("strokeStyle", a.pal.Muted)
	a.cctx.Set("fillStyle", a.pal.Muted)
	a.cctx.Set("lineWidth", 1.0)

	// No internal 3x3 grid lines: the outer band is one continuous zone, not
	// eight cells.
	a.cctx.Call("strokeRect", left+0.5, top+0.5, w-1, h-1)
	a.cctx.Call("strokeRect", innerL+0.5, innerT+0.5, tw-1, th-1)

	ccx := left + w/2
	ccy := top + h/2
	gs := math.Min(tw, th) * 0.35
	if gs < 8 {
		gs = math.Min(tw, th) * 0.5
	}
	a.cctx.Call("strokeRect", ccx-gs/2, ccy-gs/2, gs, gs)
	a.cctx.Call("strokeRect", ccx-gs/2+gs*0.25, ccy-gs/2+gs*0.25, gs, gs)

	// One arrow per band and corner cell of the implicit 3x3 grid.
	arrow := math.Min(tw, th) * 0.28
	if arrow < 8 {
		arrow = 8
	}
	drawHotspotArrow(a.cctx, left+w/2, top+th/2, 0, -arrow)
	drawHotspotArrow(a.cctx, left+w/2, top+h-th/2, 0, arrow)
	drawHotspotArrow(a.cctx, left+tw/2, top+h/2, -arrow, 0)
	drawHotspotArrow(a.cctx, left+w-tw/2, top+h/2, arrow, 0)
	d := arrow * 0.75
	drawHotspotArrow(a.cctx, left+tw/2, top+th/2, -d, -d)
	drawHotspotArrow(a.cctx, left+w-tw/2, top+th/2, d, -d)
	drawHotspotArrow(a.cctx, left+tw/2, top+h-th/2, -d, d)
	drawHotspotArrow(a.cctx, left+w-tw/2, top+h-th/2, d, d)
}

// drawHotspotArrow draws a line from (cx, cy) in direction (dx, dy), with the
// head at the far end.
func drawHotspotArrow(c js.Value, cx, cy, dx, dy float64) {
	hx := cx + dx
	hy := cy + dy
	c.Call("beginPath")
	c.Call("moveTo", cx, cy)
	c.Call("lineTo", hx, hy)
	c.Call("stroke")
	ang := math.Atan2(dy, dx)
	const headLen = 5.0
	c.Call("beginPath")
	c.Call("moveTo", hx, hy)
	c.Call("lineTo", hx+math.Cos(ang+2.5)*headLen, hy+math.Sin(ang+2.5)*headLen)
	c.Call("moveTo", hx, hy)
	c.Call("lineTo", hx+math.Cos(ang-2.5)*headLen, hy+math.Sin(ang-2.5)*headLen)
	c.Call("stroke")
}

// tileScreenRect is the on-screen rectangle of tile n as pane p draws it.
func tileScreenRect(n *gridwellv1.Tile, p *pane.Pane, r pane.Rect) (left, top, w, h float64) {
	ps := paneToDragdrop(p, r)
	left, top = ps.CellToScreen(float64(n.X), float64(n.Y))
	cellSize := cellPx * p.Zoom
	w = float64(n.W) * cellSize
	h = float64(n.H) * cellSize
	return
}

// drawTileResizePreview outlines the proposed new footprint. The original
// tile keeps painting in place, so this is a dashed stroke on top.
func (a *App) drawTileResizePreview(rd *rightDragState) {
	ps := paneToDragdrop(rd.tilePane, rd.tilePaneR)
	left, top := ps.CellToScreen(float64(rd.tileNewX), float64(rd.tileNewY))
	cellSize := cellPx * rd.tilePane.Zoom
	w := float64(rd.tileNewW) * cellSize
	h := float64(rd.tileNewH) * cellSize
	a.cctx.Set("strokeStyle", a.pal.TileResize)
	a.cctx.Set("lineWidth", 2.0)
	a.cctx.Call("setLineDash", jsArray(6, 4))
	a.cctx.Call("strokeRect", left, top, w, h)
	a.cctx.Call("setLineDash", jsArray())
	a.cctx.Set("lineWidth", 1.0)
}

// jsArray makes a JS array for the canvas dash-pattern calls.
func jsArray(vals ...float64) js.Value {
	arr := make([]any, len(vals))
	for i, v := range vals {
		arr[i] = v
	}
	return js.ValueOf(arr)
}

// drawSplitPreview draws the partition line where the split would land now.
// The side and host follow the drag, so the line flips across the grabbed
// border. Blue when a release would commit.
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

	color := a.pal.SplitInactive
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
	// A dark casing, so the line stays visible against the grey
	// markdown-preview background. Same path, stroked twice.
	a.cctx.Set("strokeStyle", a.pal.ScrimStroke)
	a.cctx.Set("lineWidth", 4.5)
	a.cctx.Call("stroke")
	a.cctx.Set("strokeStyle", color)
	a.cctx.Set("lineWidth", 2.0)
	a.cctx.Call("stroke")
	a.cctx.Set("lineWidth", 1.0)
}

// drawSplitAxisHint paints two opposing arrows along the split's axis at the
// grab point: drag either way to open a new pane on that side.
func (a *App) drawSplitAxisHint(r pane.Rect, rd *rightDragState) {
	a.cctx.Set("strokeStyle", a.pal.Muted)
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

// drawSwapPreview draws the swap affordance: a glyph at the cursor until the
// cursor lands on a different pane, then a double-headed arrow snapping to
// that pane's center.
func (a *App) drawSwapPreview(rd *rightDragState) {
	originPane := a.tree.FindPane(rd.originPaneID)
	if originPane == nil {
		return
	}
	originRect := paneRectFor(a, originPane)
	x1 := originRect.X + originRect.W/2
	y1 := originRect.Y + originRect.H/2

	// Faint, so the origin pane's content stays readable.
	a.cctx.Set("strokeStyle", a.pal.Muted)
	a.cctx.Set("lineWidth", 1.0)
	a.cctx.Call("setLineDash", jsArray(4, 4))
	a.cctx.Call("strokeRect",
		originRect.X+resizeBandPx+0.5, originRect.Y+resizeBandPx+0.5,
		originRect.W-2*resizeBandPx-1, originRect.H-2*resizeBandPx-1)
	a.cctx.Call("setLineDash", jsArray())

	destPane, destRect, ok := a.paneAtScreen(rd.curX, rd.curY)
	activeTarget := ok && destPane.ID != rd.originPaneID
	if !activeTarget {
		// No destination yet, so just the gesture identity.
		drawSwapGlyph(a.cctx, rd.curX, rd.curY, 18, a.pal.Muted)
		return
	}
	x2 := destRect.X + destRect.W/2
	y2 := destRect.Y + destRect.H/2
	a.cctx.Set("strokeStyle", a.pal.SwapArrow)
	a.cctx.Set("fillStyle", a.pal.SwapArrow)
	a.cctx.Set("lineWidth", 2.0)
	a.cctx.Call("beginPath")
	a.cctx.Call("moveTo", x1, y1)
	a.cctx.Call("lineTo", x2, y2)
	a.cctx.Call("stroke")
	a.cctx.Set("lineWidth", 1.0)
	angle := math.Atan2(y2-y1, x2-x1)
	const arrowLen = 12.0
	drawTriangle(a.cctx, x1, y1, angle+math.Pi, arrowLen)
	drawTriangle(a.cctx, x2, y2, angle, arrowLen)
}

// drawSwapGlyph paints a compact double-headed horizontal arrow.
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

// drawLeftResizePreview paints one axis per grabbed divider, so a corner grab
// shows both boundaries the drag moves. Every corridor segment the drag has
// pressed past its bump gets a red border, which a release closes.
func (a *App) drawLeftResizePreview(lr *leftResizeState) {
	for i := range lr.axes {
		a.drawResizeAxisPreview(&lr.axes[i])
	}
}

func (a *App) drawResizeAxisPreview(ax *leftResizeAxis) {
	// Live geometry every frame, because the cascade moves ancestor ratios.
	// An arm-time copy goes stale mid-drag and closes panes on a legal
	// mid-corridor release.
	root := a.tree.Root
	rootRect := a.rootLayoutRect()
	r, ok := pane.LocateSplit(root, rootRect, ax.targetSplit)
	if !ok {
		return
	}
	aRect, _ := pane.SplitRect(r, ax.splitDir, ax.targetSplit.Ratio)
	// A grey band along the shared edge, plus a double-headed arrow.
	a.cctx.Set("strokeStyle", a.pal.Muted)
	a.cctx.Set("lineWidth", 2.0)
	a.cctx.Call("setLineDash", jsArray(4, 4))
	a.cctx.Call("beginPath")
	if ax.splitDir == pane.Horizontal {
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

	// The release reads the identical stored crush.Red() state, so the red
	// set and the closed set cannot diverge.
	red := ax.crush.Red()
	if len(red) == 0 {
		return
	}
	for _, rr := range pane.SegmentRects(root, rootRect, ax.targetSplit, red) {
		strokeTileBorder(a.cctx, rr.X, rr.Y, rr.W, rr.H, a.pal.CloseWarn, paneBorderPx)
	}
	a.cctx.Set("lineWidth", 1.0)
}

// drawGhostNoEntryBadge paints the "no entry" sign over a ghost whose drop
// would be rejected.
func (a *App) drawGhostNoEntryBadge(c js.Value, cx, cy, size float64) {
	radius := size * 0.32
	if radius < 14 {
		radius = 14
	}
	ringW := radius * 0.18
	c.Set("fillStyle", a.pal.NoEntryFill)
	c.Call("beginPath")
	c.Call("arc", cx, cy, radius, 0.0, 2*math.Pi, false)
	c.Call("fill")
	c.Set("strokeStyle", a.pal.NoEntryStroke)
	c.Set("lineWidth", ringW)
	c.Call("beginPath")
	c.Call("arc", cx, cy, radius-ringW/2-1, 0.0, 2*math.Pi, false)
	c.Call("stroke")
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

// drawGhostLinkBadge paints the chain-link glyph over a ghost whose drop
// would create a cross-plugin link, so the ghost teaches that a left-drag
// links rather than copies.
func (a *App) drawGhostLinkBadge(c js.Value, cx, cy, size float64) {
	stroke := size * 0.10
	if stroke < 2 {
		stroke = 2
	}
	r := size * 0.20
	off := r * 0.55
	c.Set("strokeStyle", a.pal.PlusFg)
	c.Set("lineWidth", stroke)
	c.Call("beginPath")
	c.Call("arc", cx-off, cy, r, 0.0, 2*math.Pi, false)
	c.Call("stroke")
	c.Call("beginPath")
	c.Call("arc", cx+off, cy, r, 0.0, 2*math.Pi, false)
	c.Call("stroke")
}
