package preview

// Cover-fit geometry for URL/shell tile previews: sample an image so it
// covers the destination rect (CSS object-fit: cover), centered, then
// shifted by a pan the user can drag on a frozen descent. This was inline
// in build-tagged wasm (url_preview.go, never run by `go test`) yet it
// serves "the frozen preview pans the way you left it" — pan must never
// expose empty space past the image edges. Pure floats; testable here.

// coverWH returns the source-rect dimensions (sw, sh) that cover a
// destination of aspect ratio destAR from an iw×ih image. The larger image
// axis is cropped: a wider-than-dest image keeps full height and crops
// width; a taller one keeps full width and crops height.
func coverWH(iw, ih, destAR float64) (sw, sh float64) {
	if iw/ih > destAR {
		return ih * destAR, ih
	}
	return iw, iw / destAR
}

// CoverSrcRect returns the source rectangle (sx, sy, sw, sh) to sample from
// an iw×ih image so it covers a w×h destination, centered then shifted by
// (panX, panY) in DESTINATION pixels (panX>0 reveals the right portion,
// panY>0 the lower). ok is false for a degenerate image or destination —
// the caller should stretch-draw the whole image instead.
func CoverSrcRect(iw, ih, w, h, panX, panY float64) (sx, sy, sw, sh float64, ok bool) {
	if iw <= 0 || ih <= 0 || w <= 0 || h <= 0 {
		return 0, 0, iw, ih, false
	}
	sw, sh = coverWH(iw, ih, w/h)
	scale := sw / w // source-px per dest-px
	sx = (iw-sw)/2 + panX*scale
	sy = (ih-sh)/2 + panY*scale
	return sx, sy, sw, sh, true
}

// ClampPan clamps (panX, panY) — in destination pixels — so a cover-scaled
// iw×ih image never exposes empty space past its edges in a w×h
// destination. The center crop leaves (iw-sw)/2 source px of slack on each
// horizontal edge (and similarly vertically), which is that many over `scale`
// in destination px. A degenerate input clamps to no pan.
func ClampPan(panX, panY, iw, ih, w, h float64) (float64, float64) {
	if iw <= 0 || ih <= 0 || w <= 0 || h <= 0 {
		return 0, 0
	}
	sw, sh := coverWH(iw, ih, w/h)
	scale := sw / w
	maxPanX := (iw - sw) / (2 * scale)
	maxPanY := (ih - sh) / (2 * scale)
	return clampF(panX, -maxPanX, maxPanX), clampF(panY, -maxPanY, maxPanY)
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
