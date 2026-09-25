// Package panebox holds the geometry for a pane's interior boxes. It is
// outside client/wasm so go test exercises the math without a browser.
package panebox

import (
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/zoomtrans"
)

// LiveViewInsetPx is the one owner of the grab-gutter value. A
// WebContentsView eats all mouse input over its bounds, so the gap between two
// adjacent live panes, twice this inset, is the only canvas strip a user can
// click to grab a divider. At 5px per side that gap is about 10px, close to
// pane's resizeBandPx.
const LiveViewInsetPx = 5.0

// ContentBox returns the pane shrunk by borderPx on every side. URL tiles
// render into it and the URL stream mouse handlers hit-test against it.
func ContentBox(r pane.Rect, borderPx float64) pane.Rect {
	return InnerBox(r, borderPx)
}

// PointInContent: every live surface fills that box, as does the canvas frame
// drawn in its place while it is parked.
func PointInContent(r pane.Rect, borderPx, sx, sy float64) bool {
	return ContentBox(r, borderPx).Contains(sx, sy)
}

// LiveViewOwnsPoint is asked by every canvas pointer handler before it hands
// an event to the native surface. A WebContentsView swallows the mouse over
// the content box, unless canvasOwnsPointer (urlview.CanvasOwnsPointer: an
// armed gesture must hear its own release) or the pane has no live view, a
// frozen preview being only a canvas drawing.
func LiveViewOwnsPoint(canvasOwnsPointer, hasLiveView bool, r pane.Rect, borderPx, x, y float64) bool {
	if canvasOwnsPointer || !hasLiveView {
		return false
	}
	return PointInContent(r, borderPx, x, y)
}

// TextareaBox's sideInset is the gap between the pane edge and the text.
func TextareaBox(r pane.Rect, sideInset, baseFontPx, scale float64) (rect pane.Rect, fontPx float64) {
	return InnerBox(r, sideInset), baseFontPx * scale
}

// InnerBox is the pane inset on every side, the one body behind every box in
// this package. A pane smaller than twice the inset gives up its interior
// rather than an inside-out rectangle.
func InnerBox(r pane.Rect, inset float64) pane.Rect {
	return pane.Rect{X: r.X + inset, Y: r.Y + inset, W: max(r.W-2*inset, 0), H: max(r.H-2*inset, 0)}
}

// PointInInner reports whether (sx, sy) lies inside InnerBox(r, sideInset).
func PointInInner(r pane.Rect, sideInset, sx, sy float64) bool {
	return InnerBox(r, sideInset).Contains(sx, sy)
}

// FitZoom is zoomtrans.Fit against the pane's inner box. A degenerate inner
// box returns 1.
func FitZoom(r pane.Rect, fileW, fileH int64, sideInset, cellPx float64) float64 {
	inner := InnerBox(r, sideInset)
	if inner.W <= 0 || inner.H <= 0 {
		return 1
	}
	return zoomtrans.Fit(fileW, fileH, inner.W, inner.H, cellPx)
}

// ModalCardPos centers on the active pane rather than the screen, clamped so a
// small pane near an edge cannot push the card off the window. One larger than
// the window on an axis pins to 0, keeping its first input reachable.
func ModalCardPos(paneRect pane.Rect, cardW, cardH, winW, winH float64) (x, y float64) {
	x = paneRect.X + paneRect.W/2 - cardW/2
	y = paneRect.Y + paneRect.H/2 - cardH/2
	x = clampAxis(x, cardW, winW)
	y = clampAxis(y, cardH, winH)
	return x, y
}

// clampAxis keeps [pos, pos+size] inside [0, limit], preferring 0 when size
// exceeds limit.
func clampAxis(pos, size, limit float64) float64 { return max(min(pos, limit-size), 0) }
