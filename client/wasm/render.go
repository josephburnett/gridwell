//go:build js && wasm

package main

import (
	"fmt"
	"math"
	"strings"
	"syscall/js"

	"github.com/josephburnett/ascent/client/dragdrop"
	"github.com/josephburnett/ascent/client/pane"
	"github.com/josephburnett/ascent/internal/rpc"
)

const (
	colorBg          = "#0c0d11"
	colorPaneBorder  = "#1f2229"
	colorFocusBorder = "#4a6fff"
	colorGridLine    = "#15171d"
	colorWell        = "#1a2b3a"
	colorWellLine    = "#3a4b5a"
	colorFile        = "#2b1a3a"
	colorFileLine    = "#5a3a7a"
	colorCapped      = "#15171b"
	colorLocked      = "#26262a"
	colorSelected    = "#e3b16f"
	colorEdgeDot     = "#5a6a8a"
	colorPlusBg      = "#23252d"
	colorPlusBgHi    = "#2d3140"
	colorPlusFg      = "#c8c9ce"
	colorMenuBg      = "#16181f"
	colorMenuItem    = "#c8c9ce"
	colorMenuItemHi  = "#e8e9ee"
	colorText        = "#c8c9ce"
	colorMuted       = "#6c6f78"
)

const (
	plusButtonRadius = 18
	plusButtonInset  = 24
	menuItemHeight   = 28
	menuWidth        = 160
	menuPad          = 8
)

// menuItems lists the entries shown in the + popover, in order.
var menuItems = []string{"well", "markdown", "url", "upload"}

// draw repaints the canvas. Cheap to call repeatedly: each repaint clears
// and redraws every pane fully.
func (a *App) draw() {
	a.cctx.Set("fillStyle", colorBg)
	a.cctx.Call("fillRect", 0, 0, a.width, a.height)

	rects := a.layoutPanes()
	for paneID, r := range rects {
		p := a.tree.FindPane(paneID)
		if p == nil {
			continue
		}
		a.drawPane(p, r)
	}

	if a.dragging != nil && a.dragging.started && a.dragging.nodeID != 0 {
		a.drawDragGhost()
	}
}

// paneRect is a rectangle in screen coordinates.
type paneRect struct {
	X, Y, W, H float64
}

// layoutPanes walks the tree and assigns each leaf pane a screen rectangle.
func (a *App) layoutPanes() map[string]paneRect {
	rects := map[string]paneRect{}
	var walk func(n pane.Node, r paneRect)
	walk = func(n pane.Node, r paneRect) {
		if n.IsLeaf() {
			rects[n.Pane.ID] = r
			return
		}
		s := n.Split
		if s.Dir == pane.Horizontal {
			h1 := r.H * s.Ratio
			walk(s.A, paneRect{X: r.X, Y: r.Y, W: r.W, H: h1})
			walk(s.B, paneRect{X: r.X, Y: r.Y + h1, W: r.W, H: r.H - h1})
		} else {
			w1 := r.W * s.Ratio
			walk(s.A, paneRect{X: r.X, Y: r.Y, W: w1, H: r.H})
			walk(s.B, paneRect{X: r.X + w1, Y: r.Y, W: r.W - w1, H: r.H})
		}
	}
	walk(a.tree.Root, paneRect{X: 0, Y: 0, W: a.width, H: a.height})
	return rects
}

// drawPane draws one pane's contents.
func (a *App) drawPane(p *pane.Pane, r paneRect) {
	border := colorPaneBorder
	if p.ID == a.tree.Focus {
		border = colorFocusBorder
	}
	a.cctx.Set("strokeStyle", border)
	a.cctx.Set("lineWidth", 1.0)
	a.cctx.Call("strokeRect", r.X+0.5, r.Y+0.5, r.W-1, r.H-1)

	gid := a.gridIDForPath(p.Path)
	g, ok := a.c.Grid(gid)
	if !ok {
		a.cctx.Set("fillStyle", colorMuted)
		a.cctx.Set("font", "12px ui-monospace")
		a.cctx.Call("fillText", fmt.Sprintf("loading grid %d…", gid), r.X+12, r.Y+24)
		return
	}

	a.cctx.Call("save")
	a.cctx.Call("beginPath")
	a.cctx.Call("rect", r.X, r.Y, r.W, r.H)
	a.cctx.Call("clip")

	pscreen := dragdrop.Pane{
		ScreenX: r.X, ScreenY: r.Y, ScreenW: r.W, ScreenH: r.H,
		Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom, CellPx: cellPx,
	}

	a.drawGridLines(pscreen, r)

	cellSize := pscreen.CellPx * pscreen.Zoom

	selected := a.selectedNodeID[p.ID]
	for _, n := range g.Nodes {
		left, top := pscreen.CellToScreen(float64(n.X), float64(n.Y))
		w := float64(n.W) * cellSize
		h := float64(n.H) * cellSize
		if left+w < r.X || top+h < r.Y || left > r.X+r.W || top > r.Y+r.H {
			continue
		}
		nn := n
		drawNode(a.cctx, &nn, left, top, w, h, n.ID == selected)
	}

	// Edges to offscreen content.
	a.drawEdgeIndicators(g.Nodes, pscreen, r)

	a.cctx.Call("restore")

	// + button — drawn in screen space (after restore so it is not clipped
	// by the pane's content rect, and so it can extend a touch outside).
	a.drawPlusButton(p, r)

	// Menu, if open for this pane. Drawn last so it stacks above everything.
	if a.menuOpen && a.menuPaneID == p.ID {
		a.drawMenu(p, r)
	}
}

// drawGridLines paints faint lines at integer cell boundaries within the
// pane's visible region. Lines fade to invisible when cells are tiny so
// extreme zoom-out doesn't paint a solid grey wash.
func (a *App) drawGridLines(ps dragdrop.Pane, r paneRect) {
	cellSize := ps.CellPx * ps.Zoom
	if cellSize < 4 {
		return
	}
	// Alpha fades from 0 at cellSize=4 to 1.0 at cellSize=24.
	alpha := (cellSize - 4) / 20
	if alpha > 0.6 {
		alpha = 0.6
	}
	if alpha < 0.05 {
		return
	}
	a.cctx.Set("strokeStyle", colorGridLine)
	a.cctx.Set("lineWidth", 1.0)
	a.cctx.Set("globalAlpha", alpha)

	leftCell, topCell := ps.ScreenToCell(r.X, r.Y)
	rightCell, bottomCell := ps.ScreenToCell(r.X+r.W, r.Y+r.H)

	startX := int64(math.Floor(leftCell))
	endX := int64(math.Ceil(rightCell))
	startY := int64(math.Floor(topCell))
	endY := int64(math.Ceil(bottomCell))

	a.cctx.Call("beginPath")
	for x := startX; x <= endX; x++ {
		sx, _ := ps.CellToScreen(float64(x), 0)
		a.cctx.Call("moveTo", math.Floor(sx)+0.5, r.Y)
		a.cctx.Call("lineTo", math.Floor(sx)+0.5, r.Y+r.H)
	}
	for y := startY; y <= endY; y++ {
		_, sy := ps.CellToScreen(0, float64(y))
		a.cctx.Call("moveTo", r.X, math.Floor(sy)+0.5)
		a.cctx.Call("lineTo", r.X+r.W, math.Floor(sy)+0.5)
	}
	a.cctx.Call("stroke")
	a.cctx.Set("globalAlpha", 1.0)
}

// drawNode renders one node into the canvas at the given screen rectangle.
// `selected` highlights the node with a dedicated outline color.
func drawNode(c js.Value, n *rpc.Node, x, y, w, h float64, selected bool) {
	switch {
	case n.Type == "well" && n.Capped:
		c.Set("fillStyle", colorCapped)
		c.Call("fillRect", x, y, w, h)
		// Clip the diagonal stripes to the node rect so they don't bleed.
		c.Call("save")
		c.Call("beginPath")
		c.Call("rect", x, y, w, h)
		c.Call("clip")
		c.Set("strokeStyle", colorWellLine)
		c.Set("lineWidth", 1.0)
		// Stripes step every 8 px along the top edge; the line length
		// reaches the rect's bottom regardless of aspect ratio.
		span := w + h
		for i := -h; i < span; i += 8 {
			c.Call("beginPath")
			c.Call("moveTo", x+i, y+h)
			c.Call("lineTo", x+i+h, y)
			c.Call("stroke")
		}
		c.Call("restore")
		c.Set("strokeStyle", colorWellLine)
		c.Call("strokeRect", x, y, w, h)
	case n.Type == "well":
		c.Set("fillStyle", colorWell)
		c.Call("fillRect", x, y, w, h)
		c.Set("strokeStyle", colorWellLine)
		c.Set("lineWidth", 1.0)
		c.Call("strokeRect", x, y, w, h)
	case n.Type == "file" && n.MimeType == "text/markdown":
		c.Set("fillStyle", colorFile)
		c.Call("fillRect", x, y, w, h)
		c.Set("strokeStyle", colorFileLine)
		c.Call("strokeRect", x, y, w, h)
		c.Set("fillStyle", colorText)
		c.Set("font", "10px ui-monospace")
		c.Call("fillText", "md", x+4, y+12)
	case n.Type == "file" && strings.HasPrefix(n.MimeType, "image/"):
		c.Set("fillStyle", "#1f2229")
		c.Call("fillRect", x, y, w, h)
		c.Set("strokeStyle", colorFileLine)
		c.Call("strokeRect", x, y, w, h)
		c.Set("fillStyle", colorText)
		c.Set("font", "10px ui-monospace")
		c.Call("fillText", "img", x+4, y+12)
	case n.Type == "file" && n.MimeType == "text/uri-list":
		c.Set("fillStyle", "#1d2a1d")
		c.Call("fillRect", x, y, w, h)
		c.Set("strokeStyle", "#3a5a3a")
		c.Call("strokeRect", x, y, w, h)
		c.Set("fillStyle", colorText)
		c.Set("font", "10px ui-monospace")
		c.Call("fillText", "url", x+4, y+12)
	default:
		c.Set("fillStyle", colorLocked)
		c.Call("fillRect", x, y, w, h)
	}
	if selected {
		c.Set("strokeStyle", colorSelected)
		c.Set("lineWidth", 2.0)
		c.Call("strokeRect", x-1, y-1, w+2, h+2)
		c.Set("lineWidth", 1.0)
	}
}

// drawEdgeIndicators paints small markers along the pane's inner edge for
// every node whose footprint lies entirely outside the visible viewport.
// Each marker is positioned where the ray from the viewport center to the
// node's center crosses the pane's inset rectangle, and points outward.
func (a *App) drawEdgeIndicators(nodes map[int64]rpc.Node, ps dragdrop.Pane, r paneRect) {
	cellSize := ps.CellPx * ps.Zoom
	const inset = 12.0
	innerL := r.X + inset
	innerR := r.X + r.W - inset
	innerT := r.Y + inset
	innerB := r.Y + r.H - inset
	cx := r.X + r.W/2
	cy := r.Y + r.H/2

	a.cctx.Set("fillStyle", colorEdgeDot)
	for _, n := range nodes {
		// Node screen rect.
		sx, sy := ps.CellToScreen(float64(n.X), float64(n.Y))
		w := float64(n.W) * cellSize
		h := float64(n.H) * cellSize
		// Visible iff the rect intersects the pane.
		if sx+w > r.X && sx < r.X+r.W && sy+h > r.Y && sy < r.Y+r.H {
			continue
		}
		// Node center in screen space.
		nx := sx + w/2
		ny := sy + h/2
		// Ray from pane center to node center; intersect inset rect.
		dx := nx - cx
		dy := ny - cy
		if dx == 0 && dy == 0 {
			continue
		}
		// Find the smallest t > 0 where (cx+t*dx, cy+t*dy) hits an inner edge.
		tMax := math.MaxFloat64
		if dx > 0 {
			tMax = math.Min(tMax, (innerR-cx)/dx)
		} else if dx < 0 {
			tMax = math.Min(tMax, (innerL-cx)/dx)
		}
		if dy > 0 {
			tMax = math.Min(tMax, (innerB-cy)/dy)
		} else if dy < 0 {
			tMax = math.Min(tMax, (innerT-cy)/dy)
		}
		if tMax <= 0 || math.IsInf(tMax, 0) {
			continue
		}
		mx := cx + tMax*dx
		my := cy + tMax*dy
		// Triangle pointing away from center.
		ang := math.Atan2(dy, dx)
		a.drawTriangle(mx, my, ang, 6)
	}
}

// drawTriangle paints a small filled triangle centered at (cx, cy) pointing
// in the direction of `angle` (radians). `size` is the half-length from
// center to tip.
func (a *App) drawTriangle(cx, cy, angle, size float64) {
	// Three points: tip, then two base points behind the center perpendicular
	// to the angle.
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

// plusButtonCenter returns the screen-space center of the + button for a
// given pane.
func plusButtonCenter(r paneRect) (float64, float64) {
	return r.X + r.W - plusButtonInset, r.Y + r.H - plusButtonInset
}

// pointInPlus reports whether (x, y) lies within the + button for the given
// pane rect.
func pointInPlus(r paneRect, x, y float64) bool {
	cx, cy := plusButtonCenter(r)
	dx := x - cx
	dy := y - cy
	return dx*dx+dy*dy <= plusButtonRadius*plusButtonRadius
}

// drawPlusButton paints the floating circular + button in the pane's lower
// right.
func (a *App) drawPlusButton(p *pane.Pane, r paneRect) {
	cx, cy := plusButtonCenter(r)
	bg := colorPlusBg
	if a.menuOpen && a.menuPaneID == p.ID {
		bg = colorPlusBgHi
	}
	a.cctx.Set("fillStyle", bg)
	a.cctx.Call("beginPath")
	a.cctx.Call("arc", cx, cy, float64(plusButtonRadius), 0, 2*math.Pi)
	a.cctx.Call("fill")
	a.cctx.Set("strokeStyle", colorPaneBorder)
	a.cctx.Set("lineWidth", 1.0)
	a.cctx.Call("stroke")

	// Plus glyph: two strokes through center.
	a.cctx.Set("strokeStyle", colorPlusFg)
	a.cctx.Set("lineWidth", 2.0)
	a.cctx.Call("beginPath")
	a.cctx.Call("moveTo", cx-8, cy)
	a.cctx.Call("lineTo", cx+8, cy)
	a.cctx.Call("moveTo", cx, cy-8)
	a.cctx.Call("lineTo", cx, cy+8)
	a.cctx.Call("stroke")
	a.cctx.Set("lineWidth", 1.0)
}

// menuRect returns the screen-space rectangle the popover menu occupies for
// a given pane. The menu sits just above the + button.
func menuRect(r paneRect) (x, y, w, h float64) {
	w = float64(menuWidth)
	h = float64(menuPad*2 + menuItemHeight*len(menuItems))
	cx, cy := plusButtonCenter(r)
	x = cx + plusButtonRadius - w
	y = cy - plusButtonRadius - h - 8
	return
}

// drawMenu paints the popover menu of creation options.
func (a *App) drawMenu(p *pane.Pane, r paneRect) {
	mx, my, mw, mh := menuRect(r)
	a.cctx.Set("fillStyle", colorMenuBg)
	a.cctx.Call("fillRect", mx, my, mw, mh)
	a.cctx.Set("strokeStyle", colorPaneBorder)
	a.cctx.Set("lineWidth", 1.0)
	a.cctx.Call("strokeRect", mx+0.5, my+0.5, mw-1, mh-1)
	a.cctx.Set("font", "13px ui-monospace")
	for i, label := range menuItems {
		iy := my + float64(menuPad+i*menuItemHeight)
		// Hover highlight if cursor is over this item.
		if a.menuHover == i {
			a.cctx.Set("fillStyle", "#23252d")
			a.cctx.Call("fillRect", mx+1, iy, mw-2, float64(menuItemHeight))
		}
		color := colorMenuItem
		if a.menuHover == i {
			color = colorMenuItemHi
		}
		a.cctx.Set("fillStyle", color)
		a.cctx.Call("fillText", label, mx+menuPad+4, iy+18)
	}
	_ = p
}

// menuItemAt returns the index of the menu item at (x, y), or -1 if outside.
func menuItemAt(r paneRect, x, y float64) int {
	mx, my, mw, _ := menuRect(r)
	if x < mx || x > mx+mw {
		return -1
	}
	rel := y - (my + menuPad)
	if rel < 0 {
		return -1
	}
	idx := int(rel) / menuItemHeight
	if idx < 0 || idx >= len(menuItems) {
		return -1
	}
	return idx
}

// drawDragGhost renders a faint outline at the cursor while dragging.
func (a *App) drawDragGhost() {
	a.cctx.Set("strokeStyle", colorFocusBorder)
	a.cctx.Set("lineWidth", 1.5)
	a.cctx.Set("globalAlpha", 0.6)
	x := a.dragging.curScreenX
	y := a.dragging.curScreenY
	a.cctx.Call("strokeRect", x-16, y-16, 32, 32)
	a.cctx.Set("globalAlpha", 1.0)
	a.cctx.Set("lineWidth", 1.0)
}
