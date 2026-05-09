//go:build js && wasm

package main

import (
	"fmt"
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
	colorWell        = "#1a2b3a"
	colorWellLine    = "#3a4b5a"
	colorFile        = "#2b1a3a"
	colorFileLine    = "#5a3a7a"
	colorCapped      = "#15171b"
	colorLocked      = "#26262a"
	colorText        = "#c8c9ce"
	colorMuted       = "#6c6f78"
)

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

	if a.dragging != nil {
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
	// Border (focus highlights).
	border := colorPaneBorder
	if p.ID == a.tree.Focus {
		border = colorFocusBorder
	}
	a.cctx.Set("strokeStyle", border)
	a.cctx.Set("lineWidth", 1.0)
	a.cctx.Call("strokeRect", r.X+0.5, r.Y+0.5, r.W-1, r.H-1)

	// Find the grid this pane is looking at.
	gid := a.gridIDForPath(p.Path)
	g, ok := a.c.Grid(gid)
	if !ok {
		// Loading.
		a.cctx.Set("fillStyle", colorMuted)
		a.cctx.Set("font", "12px ui-monospace")
		a.cctx.Call("fillText", fmt.Sprintf("loading grid %d…", gid), r.X+12, r.Y+24)
		return
	}

	// Clip to the pane's rect so nodes near the edge don't bleed.
	a.cctx.Call("save")
	a.cctx.Call("beginPath")
	a.cctx.Call("rect", r.X, r.Y, r.W, r.H)
	a.cctx.Call("clip")

	pscreen := dragdrop.Pane{
		ScreenX: r.X, ScreenY: r.Y, ScreenW: r.W, ScreenH: r.H,
		Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom, CellPx: cellPx,
	}
	cellSize := pscreen.CellPx * pscreen.Zoom

	// Draw nodes.
	for _, n := range g.Nodes {
		// Skip nodes entirely outside the pane (cheap culling).
		left, top := pscreen.CellToScreen(float64(n.X), float64(n.Y))
		w := float64(n.W) * cellSize
		h := float64(n.H) * cellSize
		if left+w < r.X || top+h < r.Y || left > r.X+r.W || top > r.Y+r.H {
			continue
		}
		nn := n
		drawNode(a.cctx, &nn, left, top, w, h)
	}

	// Path breadcrumb.
	a.cctx.Set("fillStyle", colorMuted)
	a.cctx.Set("font", "11px ui-monospace")
	crumb := "/"
	for _, w := range p.Path {
		crumb += fmt.Sprintf("%d/", w)
	}
	a.cctx.Call("fillText", crumb+fmt.Sprintf(" (%dx@%.2f)", gid, p.Zoom), r.X+8, r.Y+16)

	a.cctx.Call("restore")
}

// drawNode renders one node into the canvas at the given screen rectangle.
func drawNode(c js.Value, n *rpc.Node, x, y, w, h float64) {
	switch {
	case n.Type == "well" && n.Capped:
		c.Set("fillStyle", colorCapped)
		c.Call("fillRect", x, y, w, h)
		c.Set("strokeStyle", colorWellLine)
		c.Set("lineWidth", 1.0)
		for i := -h; i < w; i += 8 {
			c.Call("beginPath")
			c.Call("moveTo", x+i, y+h)
			c.Call("lineTo", x+i+h, y)
			c.Call("stroke")
		}
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
}

// drawDragGhost renders a faint outline at the cursor while dragging.
func (a *App) drawDragGhost() {
	if a.dragging == nil {
		return
	}
	a.cctx.Set("strokeStyle", colorFocusBorder)
	a.cctx.Set("lineWidth", 1.5)
	a.cctx.Set("globalAlpha", 0.6)
	x := a.dragging.curScreenX
	y := a.dragging.curScreenY
	a.cctx.Call("strokeRect", x-16, y-16, 32, 32)
	a.cctx.Set("globalAlpha", 1.0)
}

