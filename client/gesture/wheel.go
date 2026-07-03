package gesture

// WheelAction is what a wheel event over a pane should do. The routing used
// to live inline in the wasm onWheel handler with no test; the rules are pure
// classification, so they live here (the gesture-classification home) and the
// wasm handler is glue.
type WheelAction int

const (
	// WheelZoomPane: pane-wide cursor-anchored zoom (zoomtrans.WheelZoom).
	WheelZoomPane WheelAction = iota
	// WheelScrollDoc: scroll a rendered-mode text descent vertically.
	WheelScrollDoc
	// WheelSwallow: a live URL view owns the content box; a stray wheel that
	// reaches the canvas must do NOTHING (not zoom the pane underneath).
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
}

// ClassifyWheel routes a wheel event. Outside any content descent the wheel
// zooms the pane. Inside one: a live URL view over the content box swallows
// strays (the view scrolls itself); rendered mode scrolls the document;
// anything else (text mode, a frozen url) is ignored by the canvas.
func ClassifyWheel(in WheelInput) WheelAction {
	if !in.TextFocused {
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
