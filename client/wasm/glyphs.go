//go:build js && wasm

package main

import (
	"math"
	"syscall/js"
)

// This file holds the line-icon vocabulary: the shared glyph primitives
// (sizing + stroke setup) and the per-kind glyphs the palette overlays on a
// swatch, plus the delete trashcan (drawn on the + button during a drag and
// over the shrinking delete-ghost). All pure canvas drawing — no App state.

func glyphLineWidth(w, h float64) float64 {
	return math.Max(1.0, math.Min(w, h)/34)
}

// glyphBox returns the centered square footprint (center + half-extent)
// every glyph draws within, so they sit at a uniform size side by side.
// half is ~38% of the smaller side, so a glyph mostly fills its tile — the
// same proportion a tile renders at in the menu, as a drag ghost, and once
// dropped on a grid ("things stay as you left them"). The strokes stay thin
// via glyphLineWidth, so a big glyph still reads as a clean line icon.
func glyphBox(x, y, w, h float64) (cx, cy, half float64) {
	return x + w/2, y + h/2, math.Min(w, h) * 0.38
}

// beginGlyph sets the shared stroke/fill color and round line ends.
func beginGlyph(c js.Value, w, h float64, color string) {
	c.Set("strokeStyle", color)
	c.Set("fillStyle", color)
	c.Set("lineWidth", glyphLineWidth(w, h))
	c.Set("lineCap", "round")
	c.Set("lineJoin", "round")
}

// endGlyph restores the canvas line defaults the rest of the renderer
// assumes (square caps, 1px).
func endGlyph(c js.Value) {
	c.Set("lineWidth", 1.0)
	c.Set("lineCap", "butt")
	c.Set("lineJoin", "miter")
}

// drawWellGlyph paints a 2x2 grid square — "a grid you descend into" —
// for the grid-well palette swatch.
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

// drawDocumentGlyph paints a "page with text lines" icon: a rectangle (the
// page, taller than wide) with three body-text lines, the last shorter so
// it reads as a paragraph end. Used for the markdown identity.
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
			endFrac = 0.42 // last line shorter — paragraph end
		}
		c.Call("beginPath")
		c.Call("moveTo", px+pw*0.18, ly)
		c.Call("lineTo", px+pw*0.18+pw*endFrac, ly)
		c.Call("stroke")
	}
	endGlyph(c)
}

// drawGlobeGlyph paints a stylized globe centered in (x, y, w, h):
// an outer circle, a horizontal equator, and a vertical meridian
// drawn as a narrow ellipse. Used for the URL palette tile.
func drawGlobeGlyph(c js.Value, x, y, w, h float64, color string) {
	beginGlyph(c, w, h, color)
	cx, cy, half := glyphBox(x, y, w, h)
	r := half * 0.95
	c.Call("beginPath")
	c.Call("arc", cx, cy, r, 0.0, 2*math.Pi)
	c.Call("stroke")
	// Equator.
	c.Call("beginPath")
	c.Call("moveTo", cx-r, cy)
	c.Call("lineTo", cx+r, cy)
	c.Call("stroke")
	// Vertical meridian — a narrow ellipse through the poles so it reads
	// as a curved longitude line rather than a straight bar.
	c.Call("beginPath")
	c.Call("ellipse", cx, cy, r*0.45, r, 0.0, 0.0, 2*math.Pi)
	c.Call("stroke")
	endGlyph(c)
}

// drawFolderGlyph paints a simple folder icon centered in (x, y, w, h):
// a rectangle body with a slanted tab on its upper-left edge. The whole
// outline is one closed path so the tab's right edge is part of the
// stroke (previously a separate body strokeRect left the tab open on
// the right).
func drawFolderGlyph(c js.Value, x, y, w, h float64, color string) {
	beginGlyph(c, w, h, color)
	cx, cy, half := glyphBox(x, y, w, h)
	bw := half * 2.0
	bh := half * 1.4
	bx := cx - bw/2
	by := cy - bh/2 + half*0.18 // nudge down to leave room for the tab
	tabW := bw * 0.40
	tabH := bh * 0.22
	// Outer outline, clockwise from body bottom-left.
	c.Call("beginPath")
	c.Call("moveTo", bx, by+bh)             // body bottom-left
	c.Call("lineTo", bx, by)                // up the body left side
	c.Call("lineTo", bx+tabW, by)           // right along body top to tab base
	c.Call("lineTo", bx+tabW+tabH, by-tabH) // slanted tab edge up-right
	c.Call("lineTo", bx+bw, by-tabH)        // across tab top
	c.Call("lineTo", bx+bw, by+bh)          // down body right side
	c.Call("closePath")                     // back along body bottom
	c.Call("stroke")
	endGlyph(c)
}

// drawProcessGlyph paints a small process-tree icon centered in (x, y,
// w, h): a parent node and two child nodes connected by lines. Used for
// the process-well palette tile.
func drawProcessGlyph(c js.Value, x, y, w, h float64, color string) {
	beginGlyph(c, w, h, color)
	cx, cy, half := glyphBox(x, y, w, h)
	parentY := cy - half*0.72
	childY := cy + half*0.72
	dx := half * 0.78
	nodeR := half * 0.26
	// Connectors first so the nodes sit on top.
	for _, d := range []float64{-dx, dx} {
		c.Call("beginPath")
		c.Call("moveTo", cx, parentY)
		c.Call("lineTo", cx+d, childY)
		c.Call("stroke")
	}
	// Parent + two child nodes.
	for _, p := range [][2]float64{{cx, parentY}, {cx - dx, childY}, {cx + dx, childY}} {
		c.Call("beginPath")
		c.Call("arc", p[0], p[1], nodeR, 0.0, 2*math.Pi)
		c.Call("fill")
	}
	endGlyph(c)
}

// drawShellGlyph paints a stylized terminal-prompt cue centered in
// (x, y, w, h): the chevron "›" followed by a small filled square
// suggesting a cursor block. Reads as "command prompt waiting for
// input" at any zoom. Used for the shell palette swatch.
func drawShellGlyph(c js.Value, x, y, w, h float64, color string) {
	beginGlyph(c, w, h, color)
	cx, cy, half := glyphBox(x, y, w, h)
	// Chevron ">" on the left.
	chevX := cx - half*0.5
	c.Call("beginPath")
	c.Call("moveTo", chevX-half*0.5, cy-half*0.6)
	c.Call("lineTo", chevX+half*0.25, cy)
	c.Call("lineTo", chevX-half*0.5, cy+half*0.6)
	c.Call("stroke")
	// Cursor block to the right of the chevron.
	blockW := half * 0.5
	blockH := half * 0.9
	c.Call("fillRect", cx+half*0.15, cy-blockH/2, blockW, blockH)
	endGlyph(c)
}

// drawTrashcanIcon paints a generic "trash" glyph inside (x, y, w, h).
// Pure strokes in a single colour so it stays legible at every size
// the ghost shrinks down to.
func drawTrashcanIcon(c js.Value, x, y, w, h float64) {
	// Side margin so the can doesn't touch the bounding rect.
	mx := w * 0.18
	my := h * 0.12
	left := x + mx
	right := x + w - mx
	top := y + my
	bottom := y + h - my
	bodyTop := top + (bottom-top)*0.22
	lidHeight := (bodyTop - top) * 0.55
	c.Set("strokeStyle", colorMenuItemHi)
	c.Set("fillStyle", colorMenuItemHi)
	lw := math.Max(1.5, math.Min(w, h)/22)
	c.Set("lineWidth", lw)

	// Handle on top of the lid.
	handleW := (right - left) * 0.32
	handleX := (left+right)/2 - handleW/2
	handleY := top
	c.Call("beginPath")
	c.Call("rect", handleX, handleY, handleW, lidHeight*0.7)
	c.Call("stroke")

	// Lid: a horizontal slab spanning the full width.
	c.Call("beginPath")
	c.Call("rect", left, top+lidHeight, right-left, lidHeight)
	c.Call("stroke")

	// Body: slightly tapered inward at the bottom for the classic can
	// silhouette. Stroked, not filled, so the underlying ghost (if
	// any) shows through faintly during the cross-fade.
	bodyW := right - left
	taper := bodyW * 0.06
	c.Call("beginPath")
	c.Call("moveTo", left+taper, bottom)
	c.Call("lineTo", left, bodyTop)
	c.Call("lineTo", right, bodyTop)
	c.Call("lineTo", right-taper, bottom)
	c.Call("closePath")
	c.Call("stroke")

	// Three vertical ribs inside the body — recognisable trashcan
	// detail even at small sizes.
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

// drawPaneGlyph paints a stylized split-workspace cue centered in
// (x, y, w, h): a rectangle divided by one vertical line, with the right
// half divided again horizontally — the smallest drawing that reads
// "arranged panes" at any zoom. Used for the pane-tile palette swatch and
// the face of a workspace whose layout isn't loaded (or arranged) yet.
func drawPaneGlyph(c js.Value, x, y, w, h float64, color string) {
	beginGlyph(c, w, h, color)
	cx, cy, half := glyphBox(x, y, w, h)
	left, top := cx-half, cy-half*0.75
	gw, gh := half*2, half*1.5
	c.Call("strokeRect", left, top, gw, gh)
	// Vertical divider at 45%, horizontal divider across the right half.
	vx := left + gw*0.45
	c.Call("beginPath")
	c.Call("moveTo", vx, top)
	c.Call("lineTo", vx, top+gh)
	c.Call("moveTo", vx, top+gh*0.5)
	c.Call("lineTo", left+gw, top+gh*0.5)
	c.Call("stroke")
	endGlyph(c)
}
