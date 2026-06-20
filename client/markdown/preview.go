package markdown

// This file holds the pure scale/scroll/baseline math the canvas painter
// (client/wasm/markdown_render.go) uses to render a markdown tile's preview
// and its raw-text mode. It is what keeps "preview = descent = ascent"
// honest: the frozen preview must cover-crop and place lines identically to
// the live descended pane, and the raw-text canvas must match the editing
// <textarea> to the pixel. That math was previously inline in build-tagged
// wasm (never executed by `go test`); here it is testable.

// CoverScale returns the scale that makes a boxW×boxH source region cover a
// w×h destination (CSS object-fit: cover): max(w/boxW, h/boxH). It returns 0
// for a degenerate box (≤0 on either side) so the caller can fall back to a
// fixed scale instead of dividing by zero.
func CoverScale(w, h, boxW, boxH float64) float64 {
	if boxW <= 0 || boxH <= 0 {
		return 0
	}
	s := w / boxW
	if sy := h / boxH; sy > s {
		s = sy
	}
	return s
}

// PreviewFrame is the scale + scroll offset a markdown tile preview renders
// with.
type PreviewFrame struct {
	Scale            float64
	ScrollX, ScrollY float64
}

// PreviewScaleScroll picks how a markdown tile preview is scaled and scrolled
// so it cover-crops identically to the live descended pane. Exactly one
// source applies, in priority order:
//
//   - focused: the tile is open in a focused pane — cover-crop that pane's
//     inner box (innerW × innerH), scrolled to the pane's live scroll. If the
//     inner box is degenerate, fall back to fixedScale (scroll still honored).
//   - stored framing (storedW>0 && storedH>0): cover-crop the saved window
//     (the preview = ascent return), scrolled to the saved offset.
//   - neither: scale the natural content width to fit, clamped to minScale so
//     a huge document still shows its "shape" rather than collapsing to 0.
func PreviewScaleScroll(w, h float64,
	focused bool, innerW, innerH, focusScrollX, focusScrollY float64,
	storedW, storedH, storedX, storedY int64,
	naturalPx, fixedScale, minScale float64) PreviewFrame {
	if focused {
		s := CoverScale(w, h, innerW, innerH)
		if s == 0 {
			s = fixedScale
		}
		return PreviewFrame{Scale: s, ScrollX: focusScrollX, ScrollY: focusScrollY}
	}
	if storedW > 0 && storedH > 0 {
		return PreviewFrame{
			Scale:   CoverScale(w, h, float64(storedW), float64(storedH)),
			ScrollX: float64(storedX),
			ScrollY: float64(storedY),
		}
	}
	s := w / naturalPx
	if s < minScale {
		s = minScale
	}
	return PreviewFrame{Scale: s}
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
