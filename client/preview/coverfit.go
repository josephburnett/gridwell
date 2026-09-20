package preview

// Fit geometry for URL and shell tile previews. A preview is letterboxed, the
// way CSS object-fit: contain is, never cover-cropped: the whole capture stays
// visible.

// ContainDstRect centers the capture, so bars appear on at most one axis. ok
// is false for a degenerate image or destination, and the caller then
// stretch-draws the whole image.
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

// StandinDstRect anchors a live-surface snapshot at the content box's top left
// at its intrinsic CSS size, never scaled: an xterm canvas is a whole number of
// cells and so a little smaller than its box, and contain-fitting would shift
// the terminal pixels every time the overlay parks. A non-positive dpr counts
// as 1.
func StandinDstRect(iw, ih, dpr, x, y float64) (dx, dy, dw, dh float64, ok bool) {
	if iw <= 0 || ih <= 0 {
		return 0, 0, 0, 0, false
	}
	if dpr <= 0 {
		dpr = 1
	}
	return x, y, iw / dpr, ih / dpr, true
}
