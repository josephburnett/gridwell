// Package panebox holds the pure-Go geometry for the pane's interior
// boxes — content area, file-overlay textarea, hit-tests inside the
// pane border. These were originally inlined in client/wasm; pulling
// them here lets `go test` exercise the math without a browser.
//
// All functions take `pane.Rect` so they share the same screen-space
// rectangle type as the layout / dragdrop code.
package panebox

import (
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/zoomtrans"
)

// ContentBox returns the pane's content rectangle — the pane shrunk by
// `borderPx` on every side. URL tiles render into this and the URL
// stream mouse handlers hit-test against it.
func ContentBox(r pane.Rect, borderPx float64) pane.Rect {
	x := r.X + borderPx
	y := r.Y + borderPx
	w := r.W - 2*borderPx
	h := r.H - 2*borderPx
	if w < 0 {
		w = 0
	}
	if h < 0 {
		h = 0
	}
	return pane.Rect{X: x, Y: y, W: w, H: h}
}

// PointInContent reports whether (sx, sy) lies inside ContentBox(r, borderPx).
func PointInContent(r pane.Rect, borderPx, sx, sy float64) bool {
	b := ContentBox(r, borderPx)
	return b.Contains(sx, sy)
}

// PointInURLCenter reports whether (sx, sy) lies inside the middle
// 1/3 × 1/3 of the content box. Used to restrict the URL refresh
// gesture's arm zone to the center of the content area, so right-
// clicks in the outer band still reach the split / swap / resize
// region classifier.
func PointInURLCenter(r pane.Rect, borderPx, sx, sy float64) bool {
	b := ContentBox(r, borderPx)
	x1 := b.X + b.W/3
	x2 := b.X + 2*b.W/3
	y1 := b.Y + b.H/3
	y2 := b.Y + 2*b.H/3
	return sx >= x1 && sx < x2 && sy >= y1 && sy < y2
}

// TextareaBox returns the file-overlay textarea rectangle and its
// rendered font size, both at the application's fixed text scale.
// `sideInset` is the gap between the pane edge and the text content;
// `baseFontPx` is the unscaled font size; `scale` multiplies it.
func TextareaBox(r pane.Rect, sideInset, baseFontPx, scale float64) (rect pane.Rect, fontPx float64) {
	fontPx = baseFontPx * scale
	x := r.X + sideInset
	y := r.Y + sideInset
	w := r.W - 2*sideInset
	h := r.H - 2*sideInset
	if w < 0 {
		w = 0
	}
	if h < 0 {
		h = 0
	}
	return pane.Rect{X: x, Y: y, W: w, H: h}, fontPx
}

// InnerBox is the file-focused pane's inner reading area — currently
// identical to the textarea's rectangle (without font size).
func InnerBox(r pane.Rect, sideInset float64) pane.Rect {
	b, _ := TextareaBox(r, sideInset, 0, 0)
	return b
}

// PointInInner reports whether (sx, sy) lies inside InnerBox(r, sideInset).
func PointInInner(r pane.Rect, sideInset, sx, sy float64) bool {
	return InnerBox(r, sideInset).Contains(sx, sy)
}

// OvertakeZoom returns the zoom factor at which a file of footprint
// (fileW × fileH) cells just fills the pane's inner box, given the
// base cell size `cellPx`. Returns 1 when the inner box is degenerate.
func OvertakeZoom(r pane.Rect, fileW, fileH int64, sideInset, cellPx float64) float64 {
	inner := InnerBox(r, sideInset)
	if inner.W <= 0 || inner.H <= 0 {
		return 1
	}
	return zoomtrans.Fit(fileW, fileH, inner.W, inner.H, cellPx)
}
