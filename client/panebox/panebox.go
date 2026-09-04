// Package panebox holds the pure-Go geometry for the pane's interior boxes:
// content area, text-overlay textarea, and hit-tests inside the pane border.
// It lives outside client/wasm so `go test` exercises the math without a
// browser.
//
// All functions take `pane.Rect` so they share the same screen-space
// rectangle type as the layout / dragdrop code.
package panebox

import (
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/zoomtrans"
)

// LiveViewInsetPx is the inset applied on every side of a pane's live content
// view (URL WebContentsView, shell overlay). It is the single owner of the
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

// PointInContent is the hit-test twin of ContentBox: it reports whether
// (sx, sy) lies inside ContentBox(r, borderPx). A live surface — a url or
// page view, a shell overlay — fills the same box, as does the canvas frame
// drawn in its place while it is parked, so a parked frame lands exactly
// where the live view was instead of jumping.
func PointInContent(r pane.Rect, borderPx, sx, sy float64) bool {
	return ContentBox(r, borderPx).Contains(sx, sy)
}

// LiveViewOwnsPoint is the one owner of "does this pane's live view own this
// screen point" — the question every canvas pointer handler must ask before it
// hands an event to the native surface instead of acting on it itself.
//
// A live view (a url tile's WebContentsView) paints over the pane's content
// box and swallows the mouse there, so a point inside that box belongs to the
// page and not to the canvas. Two facts unmake that, and both are inputs here
// rather than assumptions at the call site:
//
//   - overlaysHidden — the client parks every live view while a gesture is
//     armed, the + menu is open, or the url modal is up (the shim's
//     liveOverlaysHidden, the one owner of that state). A parked view paints
//     nothing and owns nothing, so the canvas keeps every event over it,
//     including the release that ends the very gesture that parked it. A
//     handler that swallows without asking discards that release and leaves
//     the gesture armed forever.
//   - hasLiveView — a frozen preview is a canvas drawing. Only a live view
//     owns pixels.
func LiveViewOwnsPoint(overlaysHidden, hasLiveView bool, r pane.Rect, borderPx, x, y float64) bool {
	if overlaysHidden || !hasLiveView {
		return false
	}
	return PointInContent(r, borderPx, x, y)
}

// TextareaBox returns the text-overlay textarea rectangle and its
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

// InnerBox is the text-focused pane's inner reading area, identical to the
// textarea's rectangle without the font size.
func InnerBox(r pane.Rect, sideInset float64) pane.Rect {
	b, _ := TextareaBox(r, sideInset, 0, 0)
	return b
}

// PointInInner reports whether (sx, sy) lies inside InnerBox(r, sideInset).
func PointInInner(r pane.Rect, sideInset, sx, sy float64) bool {
	return InnerBox(r, sideInset).Contains(sx, sy)
}

// FitZoom returns the zoom factor at which a text tile of footprint
// (fileW × fileH) cells just fits the pane's inner box: zoomtrans.Fit, the
// min dimension ratio. Returns 1 when the inner box is degenerate.
func FitZoom(r pane.Rect, fileW, fileH int64, sideInset, cellPx float64) float64 {
	inner := InnerBox(r, sideInset)
	if inner.W <= 0 || inner.H <= 0 {
		return 1
	}
	return zoomtrans.Fit(fileW, fileH, inner.W, inner.H, cellPx)
}

// ModalCardPos places a modal card centered on the active pane rather than
// the screen: the pane you acted in is where the dialog appears. Returns the
// card's top-left, clamped so the card stays fully on-screen, since a small
// pane near an edge must not push it off the window. When the card is larger
// than the window on an axis it pins to 0, so the top-left — where the first
// input lives — stays reachable.
func ModalCardPos(paneRect pane.Rect, cardW, cardH, winW, winH float64) (x, y float64) {
	x = paneRect.X + paneRect.W/2 - cardW/2
	y = paneRect.Y + paneRect.H/2 - cardH/2
	x = clampAxis(x, cardW, winW)
	y = clampAxis(y, cardH, winH)
	return x, y
}

// clampAxis keeps [pos, pos+size] inside [0, limit], preferring 0 when size
// exceeds limit.
func clampAxis(pos, size, limit float64) float64 {
	if pos+size > limit {
		pos = limit - size
	}
	if pos < 0 {
		pos = 0
	}
	return pos
}
