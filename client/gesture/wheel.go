package gesture

// WheelAction is what a wheel event over a pane should do. The rules are pure
// classification, so they live here and the wasm onWheel handler is glue.
type WheelAction int

const (
	// WheelZoomPane: pane-wide cursor-anchored zoom (zoomtrans.WheelZoom).
	WheelZoomPane WheelAction = iota
	// WheelZoomWell: the cursor hovers an enterable well in a grid view, so
	// the wheel zooms the grid inside the well (its stored preview framing),
	// not the grid the pane shows. Empty space is the escape hatch back to
	// the pane zoom.
	WheelZoomWell
	// WheelScrollDoc: scroll a rendered-mode text descent vertically.
	WheelScrollDoc
	// WheelSwallow: a live URL view owns the content box, so a stray wheel
	// that reaches the canvas does nothing — in particular it must not zoom
	// the pane underneath.
	WheelSwallow
	// WheelIgnore: no canvas action (e.g. text-mode focus — the textarea
	// overlay handles its own scrolling and this event is stray).
	WheelIgnore
)

// WheelInput is the resolved state the routing decides on. The caller (wasm)
// resolves the impure facts; the rules order them here.
type WheelInput struct {
	// TextFocused: the pane is descended into a content tile (TextFocus set).
	TextFocused bool
	// URLDescent: that descent is into a url tile.
	URLDescent bool
	// LiveURLView: a native WebContentsView is attached for this pane.
	LiveURLView bool
	// InContentBox: the cursor is inside the pane's content box.
	InContentBox bool
	// TextModeRendered: the text descent is in rendered mode.
	TextModeRendered bool
	// OverEnterableWell: in a grid view, the cursor is over a well tile
	// with a resolvable child grid (the same predicate a drop's
	// PromoteToWell uses).
	OverEnterableWell bool
	// ZoomOut: the wheel direction shrinks (deltaY > 0 in
	// zoomtrans.WheelZoom's convention).
	ZoomOut bool
	// WellCoverage: how much of the pane's content box the hovered well's
	// on-screen rect covers, 0..1 (RectCoverage). Meaningful only with
	// OverEnterableWell.
	WellCoverage float64
}

// WellZoomOutRedirect is the coverage past which zooming out over a well goes
// to the pane instead of the well: when a single well fills most of the view
// there is no visible outer context, and wheel-out inside it reads as "let me
// back out". Zoom in keeps the well-preview behavior at any coverage, because
// leaning in is about the well.
const WellZoomOutRedirect = 0.5

// RectCoverage reports how much of the box (bx,by,bw,bh) the rect
// (rx,ry,rw,rh) covers, as intersection area / box area in 0..1. The one
// geometry rule behind WellZoomOutRedirect.
func RectCoverage(rx, ry, rw, rh, bx, by, bw, bh float64) float64 {
	if bw <= 0 || bh <= 0 {
		return 0
	}
	ix := max(rx, bx)
	iy := max(ry, by)
	ix2 := min(rx+rw, bx+bw)
	iy2 := min(ry+rh, by+bh)
	if ix2 <= ix || iy2 <= iy {
		return 0
	}
	return ((ix2 - ix) * (iy2 - iy)) / (bw * bh)
}

// ClassifyWheel routes a wheel event. Outside any content descent the wheel
// zooms the pane, unless the cursor hovers an enterable well, whose own
// preview zooms instead; empty space still zooms the pane, and zooming out
// over a well that covers most of the view redirects to the pane
// (WellZoomOutRedirect). Inside a descent: a live URL view over the content
// box swallows strays, since the view scrolls itself; rendered mode scrolls
// the document; anything else (text mode, a frozen url) is ignored by the
// canvas.
func ClassifyWheel(in WheelInput) WheelAction {
	if !in.TextFocused {
		if in.OverEnterableWell {
			if in.ZoomOut && in.WellCoverage > WellZoomOutRedirect {
				return WheelZoomPane
			}
			return WheelZoomWell
		}
		return WheelZoomPane
	}
	if in.URLDescent && in.LiveURLView && in.InContentBox {
		return WheelSwallow
	}
	if in.TextModeRendered {
		return WheelScrollDoc
	}
	return WheelIgnore
}
