package markdown

// This file holds the pure scale/scroll/baseline math the canvas painter
// (client/wasm/markdown_render.go) uses to render a markdown tile's preview
// and its raw-text mode. It is what keeps "preview = descent = ascent"
// honest: the frozen preview places lines identically to the live descended
// pane, and the raw-text canvas matches the editing <textarea> to the pixel.
// It lives here, not in the wasm shim, so `go test` executes it.

// PreviewFrame is the scale + scroll offset a markdown tile preview renders
// with.
type PreviewFrame struct {
	Scale            float64
	ScrollX, ScrollY float64
	// ContentW is the logical width the markdown is laid out at for this
	// frame: the tile's own inner width divided by Scale, so the doc wraps to
	// the tile like a window and the painter merely scales the ops back up.
	ContentW float64
}

// PreviewBodyLinePx is one body-text line's height at scale 1 (14px body ×
// 1.35 line spacing, rounded up) — the unit PreviewContentVisible gates on.
const PreviewBodyLinePx = 19.0

// PreviewWindowFrame is how a text tile preview is scaled: constant type size
// — like the alt-text banner, the font never follows grid zoom — wrapped to
// the tile's inner width and clipped to its height, so the tile is a window
// that reveals more of the document as it grows. fixedScale × contentZoom is
// the whole scale; content_zoom is the one owner of "make the text big".
// Scroll comes from the stored framing, so the preview keeps showing the
// place you left.
func PreviewWindowFrame(innerW, fixedScale, contentZoom float64, storedX, storedY int64) PreviewFrame {
	s := fixedScale * contentZoom
	if s <= 0 {
		s = fixedScale
	}
	return PreviewFrame{
		Scale:    s,
		ScrollX:  float64(storedX),
		ScrollY:  float64(storedY),
		ContentW: innerW / s,
	}
}

// PreviewContentVisible gates the content paint: with the type size
// constant, a small tile cannot show a legible line, so below one body line
// of room the preview is the alt-text banner alone. This mirrors the well's
// previewCell >= 0.5 level-of-detail gate. availH is the tile's inner height
// minus whatever the banner occupies.
func PreviewContentVisible(availH, scale float64) bool {
	return availH >= PreviewBodyLinePx*scale
}

// RawTextSlot holds the per-line placement for monospace raw-text rendering,
// derived to match the editing <textarea>'s CSS line boxes to the pixel.
type RawTextSlot struct {
	Slot     float64 // line advance, in scaled px
	Baseline float64 // alphabetic-baseline offset from a slot's top
	Top0     float64 // top of the first line's slot, in scaled px
}

// RawTextLineSlot computes the line slot geometry for raw monospace text.
// A CSS line box is lineHeight (= fontPx × lineHeightMul) tall; the font's
// content area (asc+desc, from the canvas's measureText in the already-scaled
// font) is centered in it with equal leading, and the alphabetic baseline
// sits one ascent below the content-area top. Everything is scaled by `scale`
// so the canvas preview matches the focused textarea exactly.
func RawTextLineSlot(fontPx, lineHeightMul, scale, pad, scrollY, asc, desc float64) RawTextSlot {
	slot := fontPx * lineHeightMul * scale
	return RawTextSlot{
		Slot:     slot,
		Baseline: (slot-(asc+desc))/2 + asc,
		Top0:     (pad - scrollY) * scale,
	}
}

// RawTextLineVisible reports whether a line whose slot starts at slotTop (in
// the pane's local px, y-down) overlaps the visible band [0, h). Used to skip
// fillText for off-screen lines.
func RawTextLineVisible(slotTop, slot, h float64) bool {
	return slotTop+slot > 0 && slotTop < h
}
