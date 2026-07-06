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

// LiveViewInsetPx is the inset applied on every side of a pane's live content
// view (URL WebContentsView, shell overlay). It is the SINGLE owner of the
// grab-gutter value.
//
// The gap between two horizontally adjacent live panes = 2 × LiveViewInsetPx;
// that is the only canvas strip a user can click to grab a divider, because
// a WebContentsView eats all mouse input over its bounds. At 5px per side the
// gap is ~10px — approximately equal to resizeBandPx, making it comfortably
// grabbable. Both client/wasm render.go (canvas clip + border drawing) and
// shell_stream_client.go (shell overlay placement) read this constant via
// ContentBox rather than defining their own copies.
const LiveViewInsetPx = 5.0

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

// FitZoom returns the zoom factor at which a text tile of footprint
// (fileW × fileH) cells just FITS the pane's inner box (zoomtrans.Fit — the
// min dim ratio; formerly misnamed OvertakeZoom, which is the max). Returns
// 1 when the inner box is degenerate.
func FitZoom(r pane.Rect, fileW, fileH int64, sideInset, cellPx float64) float64 {
	inner := InnerBox(r, sideInset)
	if inner.W <= 0 || inner.H <= 0 {
		return 1
	}
	return zoomtrans.Fit(fileW, fileH, inner.W, inner.H, cellPx)
}
