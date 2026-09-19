//go:build js && wasm

package main

import (
	"math"
	"syscall/js"
)

// The line-icon vocabulary: the shared glyph primitives, the per-kind glyphs
// the palette overlays on a swatch, and the delete trashcan. Pure canvas
// drawing, no App state. Every identity glyph shares one visual spec so the
// creation menu reads as a set, bracketed by beginGlyph and endGlyph.

// glyphLineWidth ties the stroke weight to tile size, so icons scale with
// their swatch but read at the same relative weight.
func glyphLineWidth(w, h float64) float64 {
	return math.Max(1.0, math.Min(w, h)/34)
}

// glyphBox is the centered square footprint every glyph draws within, so
// they sit at a uniform size side by side. half is about 38% of the smaller
// side, the same proportion a tile renders at in the menu, as a drag ghost,
// and once dropped.
func glyphBox(x, y, w, h float64) (cx, cy, half float64) {
	return x + w/2, y + h/2, math.Min(w, h) * 0.38
}

func beginGlyph(c js.Value, w, h float64, color string) {
	c.Set("strokeStyle", color)
	c.Set("fillStyle", color)
	c.Set("lineWidth", glyphLineWidth(w, h))
	c.Set("lineCap", "round")
	c.Set("lineJoin", "round")
}

// beginSlotGlyph is the same bracket at the fixed 2px weight the bar-slot
// glyphs draw at, where the stroke sizes to the button circle rather than to
// a tile.
func beginSlotGlyph(c js.Value, color string) {
	c.Set("strokeStyle", color)
	c.Set("lineWidth", 2.0)
	c.Set("lineCap", "round")
	c.Set("lineJoin", "round")
}

// endGlyph restores the canvas line defaults the rest of the renderer
// assumes.
func endGlyph(c js.Value) {
	c.Set("lineWidth", 1.0)
	c.Set("lineCap", "butt")
	c.Set("lineJoin", "miter")
}

// drawWellGlyph paints a 2x2 grid square: a grid you descend into.
func drawWellGlyph(c js.Value, x, y, w, h float64, color string) {
	beginGlyph(c, w, h, color)
	cx, cy, half := glyphBox(x, y, w, h)
	c.Call("strokeRect", cx-half, cy-half, half*2, half*2)
	c.Call("beginPath")
	c.Call("moveTo", cx, cy-half)
	c.Call("lineTo", cx, cy+half)
	c.Call("moveTo", cx-half, cy)
	c.Call("lineTo", cx+half, cy)
	c.Call("stroke")
	endGlyph(c)
}

// drawDocumentGlyph paints a page with three text lines, the last shorter so
// it reads as a paragraph end.
func drawDocumentGlyph(c js.Value, x, y, w, h float64, color string) {
	beginGlyph(c, w, h, color)
	cx, cy, half := glyphBox(x, y, w, h)
	pw := half * 1.5
	ph := half * 2.0
	px := cx - pw/2
	py := cy - ph/2
	c.Call("strokeRect", px, py, pw, ph)
	for i, frac := range []float64{0.30, 0.50, 0.70} {
		ly := py + ph*frac
		endFrac := 0.66
		if i == 2 {
			endFrac = 0.42 // paragraph end
		}
		c.Call("beginPath")
		c.Call("moveTo", px+pw*0.18, ly)
		c.Call("lineTo", px+pw*0.18+pw*endFrac, ly)
		c.Call("stroke")
	}
	endGlyph(c)
}

// drawGlobeGlyph paints a globe: a circle, an equator, and a meridian drawn
// as a narrow ellipse.
func drawGlobeGlyph(c js.Value, x, y, w, h float64, color string) {
	beginGlyph(c, w, h, color)
	cx, cy, half := glyphBox(x, y, w, h)
	r := half * 0.95
	c.Call("beginPath")
	c.Call("arc", cx, cy, r, 0.0, 2*math.Pi)
	c.Call("stroke")
	c.Call("beginPath")
	c.Call("moveTo", cx-r, cy)
	c.Call("lineTo", cx+r, cy)
	c.Call("stroke")
	// An ellipse, so the meridian reads as a curved longitude line rather
	// than a straight bar.
	c.Call("beginPath")
	c.Call("ellipse", cx, cy, r*0.45, r, 0.0, 0.0, 2*math.Pi)
	c.Call("stroke")
	endGlyph(c)
}

// drawFolderGlyph paints a folder: a body with a slanted tab on its
// upper-left edge. One closed path, because a separate body strokeRect would
// leave the tab open on the right.
func drawFolderGlyph(c js.Value, x, y, w, h float64, color string) {
	beginGlyph(c, w, h, color)
	cx, cy, half := glyphBox(x, y, w, h)
	bw := half * 2.0
	bh := half * 1.4
	bx := cx - bw/2
	by := cy - bh/2 + half*0.18 // room for the tab
	tabW := bw * 0.40
	tabH := bh * 0.22
	c.Call("beginPath")
	c.Call("moveTo", bx, by+bh)
	c.Call("lineTo", bx, by)
	c.Call("lineTo", bx+tabW, by)
	c.Call("lineTo", bx+tabW+tabH, by-tabH)
	c.Call("lineTo", bx+bw, by-tabH)
	c.Call("lineTo", bx+bw, by+bh)
	c.Call("closePath")
	c.Call("stroke")
	endGlyph(c)
}

// drawProcessGlyph paints a process tree: a parent node and two children
// connected by lines.
func drawProcessGlyph(c js.Value, x, y, w, h float64, color string) {
	beginGlyph(c, w, h, color)
	cx, cy, half := glyphBox(x, y, w, h)
	parentY := cy - half*0.72
	childY := cy + half*0.72
	dx := half * 0.78
	nodeR := half * 0.26
	// Connectors first, so the nodes sit on top.
	for _, d := range []float64{-dx, dx} {
		c.Call("beginPath")
		c.Call("moveTo", cx, parentY)
		c.Call("lineTo", cx+d, childY)
		c.Call("stroke")
	}
	for _, p := range [][2]float64{{cx, parentY}, {cx - dx, childY}, {cx + dx, childY}} {
		c.Call("beginPath")
		c.Call("arc", p[0], p[1], nodeR, 0.0, 2*math.Pi)
		c.Call("fill")
	}
	endGlyph(c)
}

// drawShellGlyph paints a terminal prompt: a chevron followed by a cursor
// block.
func drawShellGlyph(c js.Value, x, y, w, h float64, color string) {
	beginGlyph(c, w, h, color)
	cx, cy, half := glyphBox(x, y, w, h)
	chevX := cx - half*0.5
	c.Call("beginPath")
	c.Call("moveTo", chevX-half*0.5, cy-half*0.6)
	c.Call("lineTo", chevX+half*0.25, cy)
	c.Call("lineTo", chevX-half*0.5, cy+half*0.6)
	c.Call("stroke")
	blockW := half * 0.5
	blockH := half * 0.9
	c.Call("fillRect", cx+half*0.15, cy-blockH/2, blockW, blockH)
	endGlyph(c)
}

// drawTrashcanIcon paints a trash glyph in pure strokes, so it stays legible
// at every size the ghost shrinks to.
func (a *App) drawTrashcanIcon(c js.Value, x, y, w, h float64) {
	// A margin, so the can does not touch the bounding rect.
	mx := w * 0.18
	my := h * 0.12
	left := x + mx
	right := x + w - mx
	top := y + my
	bottom := y + h - my
	bodyTop := top + (bottom-top)*0.22
	lidHeight := (bodyTop - top) * 0.55
	c.Set("strokeStyle", a.pal.MenuItemHi)
	c.Set("fillStyle", a.pal.MenuItemHi)
	lw := math.Max(1.5, math.Min(w, h)/22)
	c.Set("lineWidth", lw)

	handleW := (right - left) * 0.32
	handleX := (left+right)/2 - handleW/2
	handleY := top
	c.Call("beginPath")
	c.Call("rect", handleX, handleY, handleW, lidHeight*0.7)
	c.Call("stroke")

	c.Call("beginPath")
	c.Call("rect", left, top+lidHeight, right-left, lidHeight)
	c.Call("stroke")

	// Stroked, not filled, so the underlying ghost shows through during the
	// cross-fade.
	bodyW := right - left
	taper := bodyW * 0.06
	c.Call("beginPath")
	c.Call("moveTo", left+taper, bottom)
	c.Call("lineTo", left, bodyTop)
	c.Call("lineTo", right, bodyTop)
	c.Call("lineTo", right-taper, bottom)
	c.Call("closePath")
	c.Call("stroke")

	ribSpacing := (bodyW - 2*taper) / 4
	for i := 1; i <= 3; i++ {
		rx := left + taper + ribSpacing*float64(i)
		c.Call("beginPath")
		c.Call("moveTo", rx, bodyTop+(bottom-bodyTop)*0.18)
		c.Call("lineTo", rx-taper*0.7, bottom-(bottom-bodyTop)*0.08)
		c.Call("stroke")
	}
	c.Set("lineWidth", 1.0)
}

// drawPaneGlyph paints a split layout: a rectangle divided vertically, its
// right half divided again. Also the face of a pane tile whose layout is not
// loaded or not arranged yet.
func drawPaneGlyph(c js.Value, x, y, w, h float64, color string) {
	beginGlyph(c, w, h, color)
	cx, cy, half := glyphBox(x, y, w, h)
	left, top := cx-half, cy-half*0.75
	gw, gh := half*2, half*1.5
	c.Call("strokeRect", left, top, gw, gh)
	vx := left + gw*0.45
	c.Call("beginPath")
	c.Call("moveTo", vx, top)
	c.Call("lineTo", vx, top+gh)
	c.Call("moveTo", vx, top+gh*0.5)
	c.Call("lineTo", left+gw, top+gh*0.5)
	c.Call("stroke")
	endGlyph(c)
}

// drawTrashGlyph paints a tapered bin with a lid line and handle, declared by
// the home's trash root entry.
func drawTrashGlyph(c js.Value, x, y, w, h float64, color string) {
	beginGlyph(c, w, h, color)
	cx, cy, half := glyphBox(x, y, w, h)
	bw := half * 1.5
	bh := half * 1.5
	topY := cy - bh/2 + half*0.25
	botY := topY + bh
	taper := bw * 0.12
	c.Call("beginPath")
	c.Call("moveTo", cx-bw/2, topY)
	c.Call("lineTo", cx+bw/2, topY)
	c.Call("lineTo", cx+bw/2-taper, botY)
	c.Call("lineTo", cx-bw/2+taper, botY)
	c.Call("closePath")
	c.Call("stroke")
	lidY := topY - bh*0.14
	c.Call("beginPath")
	c.Call("moveTo", cx-bw*0.62, lidY)
	c.Call("lineTo", cx+bw*0.62, lidY)
	c.Call("stroke")
	c.Call("beginPath")
	c.Call("moveTo", cx-bw*0.18, lidY)
	c.Call("lineTo", cx-bw*0.10, lidY-bh*0.16)
	c.Call("lineTo", cx+bw*0.10, lidY-bh*0.16)
	c.Call("lineTo", cx+bw*0.18, lidY)
	c.Call("stroke")
	endGlyph(c)
}
