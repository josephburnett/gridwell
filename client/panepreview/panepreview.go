// Package panepreview computes the geometry of a pane tile's live
// mini-render: the stored pane layout drawn small inside the tile's rect.
// Pure Go, so the preview↔descent continuity — the pane-tile face of
// "preview = descent target" — is provable headlessly.
//
// The preview is the live workspace scaled uniformly by Scale(): pane.Layout
// is affine in its root rect, so each leaf's preview rect is exactly its
// live rect under that scaling, and each leaf's content cell size is its
// live cell size times the same factor. Descending into the pane tile
// therefore lands on exactly what the preview showed, only bigger.
package panepreview

import "github.com/josephburnett/gridwell/client/pane"

// Leaf is one pane of the mini-render: the leaf's state, its rect inside the
// tile, and the cell size its grid content draws at.
type Leaf struct {
	Pane *pane.Pane
	Rect pane.Rect
	// PreviewCell is the on-screen size of one grid cell inside this leaf:
	// the leaf's live cell size (Zoom × CellPx) shrunk by the tile scale.
	PreviewCell float64
}

// Scale returns the uniform factor by which the live workspace shrinks into
// the tile rect: min of the width and height ratios, so the whole layout
// fits without distortion. Zero when the live rect is degenerate.
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

// Leaves lays the tree out into the tile rect and pairs each leaf with its
// preview transform. Layout itself honors Zoomed (a zoomed pane owns the
// whole rect), so the mini-render shows exactly what descent would restore.
func Leaves(t *pane.Tree, tileRect pane.Rect, scale float64) []Leaf {
	rects := pane.Layout(t, tileRect)
	var out []Leaf
	t.Walk(func(p *pane.Pane) {
		r, ok := rects[p.ID]
		if !ok {
			return // zoomed: only the zoomed leaf has a rect
		}
		out = append(out, Leaf{
			Pane:        p,
			Rect:        r,
			PreviewCell: p.Zoom * pane.CellPx * scale,
		})
	})
	return out
}
