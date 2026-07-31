package preview

// Fit geometry for URL/shell tile previews: scale an image uniformly so it
// FITS inside the destination rect (CSS object-fit: contain), centered, with
// letterbox/pillarbox bars filling the remainder. The owner decided (issue
// #89) that previews whose aspect differs radically from the pane read better
// letterboxed than cover-cropped — you see the whole capture, never a crop.
// (The previous cover-crop + draggable pan lived here as CoverSrcRect /
// ClampPan; under fit there is no overflow to pan into, so both — and the pan
// gesture — were removed.) Pure floats; testable without a canvas.

// ContainDstRect returns the destination sub-rectangle (dx, dy, dw, dh)
// inside (x, y, w, h) that an iw×ih image fills when scaled uniformly to fit:
// scaled by the smaller axis ratio and centered, so bars appear on exactly
// one axis (or none when aspects match). ok is false for a degenerate image
// or destination — the caller should stretch-draw the whole image instead.
func ContainDstRect(iw, ih, x, y, w, h float64) (dx, dy, dw, dh float64, ok bool) {
	if iw <= 0 || ih <= 0 || w <= 0 || h <= 0 {
		return x, y, w, h, false
	}
	scale := w / iw
	if s := h / ih; s < scale {
		scale = s
	}
	dw = iw * scale
	dh = ih * scale
	return x + (w-dw)/2, y + (h-dh)/2, dw, dh, true
}

// StandinDstRect places a live-surface snapshot back where the live surface
// drew it: top-left anchored inside the content box at intrinsic CSS size
// (natural pixels over the capture-time device pixel ratio), never scaled.
// A live xterm canvas is an integer number of cells — always a little
// smaller than its content box — so contain-fitting its snapshot would
// center it and scale it up by the leftover cell fraction, visibly shifting
// the terminal pixels every time the overlay parks (issue #224). ok is
// false for a degenerate image; a non-positive dpr is treated as 1.
func StandinDstRect(iw, ih, dpr, x, y float64) (dx, dy, dw, dh float64, ok bool) {
	if iw <= 0 || ih <= 0 {
		return 0, 0, 0, 0, false
	}
	if dpr <= 0 {
		dpr = 1
	}
	return x, y, iw / dpr, ih / dpr, true
}
