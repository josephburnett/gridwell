// Package panepreview owns a pane tile's face from outside: the memo of its
// decoded layout (layouts.go) and the geometry of its mini-render. The
// preview is the live workspace shrunk uniformly by Scale, and pane.Layout is
// affine in its root rect, so descending into the tile lands on what the
// preview showed, only bigger.
package panepreview

import "github.com/josephburnett/gridwell/client/pane"

// Leaf is one pane of the mini-render.
type Leaf struct {
	Pane *pane.Pane
	Rect pane.Rect
	// PreviewCell is the live cell size (Zoom × CellPx) shrunk by the tile
	// scale.
	PreviewCell float64
}

// Scale is the smaller of the width and height ratios, so the layout fits
// without distortion. A degenerate live rect returns zero.
func Scale(tileRect, liveRootRect pane.Rect) float64 {
	if liveRootRect.W <= 0 || liveRootRect.H <= 0 {
		return 0
	}
	sx := tileRect.W / liveRootRect.W
	sy := tileRect.H / liveRootRect.H
	if sx < sy {
		return sx
	}
	return sy
}

// Leaves pairs each leaf with its preview transform. pane.Layout honors
// Zoomed, so the mini-render shows what descent would restore.
func Leaves(t *pane.Tree, tileRect pane.Rect, scale float64) []Leaf {
	rects := pane.Layout(t, tileRect)
	var out []Leaf
	t.Walk(func(p *pane.Pane) {
		r, ok := rects[p.ID]
		if !ok {
			return // when a pane is zoomed only that leaf has a rect
		}
		out = append(out, Leaf{
			Pane:        p,
			Rect:        r,
			PreviewCell: p.Zoom * pane.CellPx * scale,
		})
	})
	return out
}
