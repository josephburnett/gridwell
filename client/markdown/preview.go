package markdown

// The scale, scroll and baseline math for a markdown tile's preview and its
// raw-text mode. It is outside the canvas painter so go test executes it, and
// so preview, descent and the editing <textarea> place lines identically.

import "math"

// PreviewFrame is a markdown preview's scale and scroll offset.
type PreviewFrame struct {
	Scale            float64
	ScrollX, ScrollY float64
	// ContentW is the tile's inner width over Scale, so the painter only
	// scales the ops back up.
	ContentW float64
}

// PreviewBodyLinePx is 14px body times 1.35 line spacing, rounded up.
const PreviewBodyLinePx = 19.0

// PreviewWindowFrame keeps the type size constant, so the font never follows
// grid zoom and the tile is a window revealing more as it grows. content_zoom
// is the one owner of making text bigger.
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

// RasterDraw is one drawImage call: a source rect in the raster's logical px
// and the dest rect on canvas.
type RasterDraw struct {
	SX, SY, SW, SH float64
	DX, DY, DW, DH float64
}

// PreviewRasterDraw windows a raster rasterW by rasterH logical px into the
// box below topInset, at frame.Scale and never at the box's width over the
// raster's, so the letters keep their size whatever width the raster was made
// at: the box clips a wider raster and leaves a margin beside a narrower one.
// False means the window has scrolled off the raster.
func PreviewRasterDraw(frame PreviewFrame, rasterW, rasterH, x, y, w, h, topInset float64) (RasterDraw, bool) {
	s := frame.Scale
	sx, sy := frame.ScrollX, frame.ScrollY
	if s <= 0 || sx < 0 || sy < 0 || sx >= rasterW || sy >= rasterH {
		return RasterDraw{}, false
	}
	sw := math.Min(w/s, rasterW-sx)
	sh := math.Min((h-topInset)/s, rasterH-sy)
	if sw <= 0 || sh <= 0 {
		return RasterDraw{}, false
	}
	return RasterDraw{
		SX: sx, SY: sy, SW: sw, SH: sh,
		DX: x, DY: y + topInset, DW: sw * s, DH: sh * s,
	}, true
}

// PreviewContentVisible drops to the alt-text banner alone below one body line
// of room, mirroring the well's previewCell >= 0.5 gate. availH is the inner
// height minus the banner.
func PreviewContentVisible(availH, scale float64) bool {
	return availH >= PreviewBodyLinePx*scale
}

// RawTextSlot matches the editing <textarea>'s CSS line boxes to the pixel.
type RawTextSlot struct {
	Slot     float64 // line advance, in scaled px
	Baseline float64 // alphabetic-baseline offset from a slot's top
	Top0     float64 // top of the first line's slot, in scaled px
}

// RawTextLineSlot follows CSS: a line box is fontPx × lineHeightMul tall, the
// font's content area is centered in it, and the alphabetic baseline sits one
// ascent below that area's top.
func RawTextLineSlot(fontPx, lineHeightMul, scale, pad, scrollY, asc, desc float64) RawTextSlot {
	slot := fontPx * lineHeightMul * scale
	return RawTextSlot{
		Slot:     slot,
		Baseline: (slot-(asc+desc))/2 + asc,
		Top0:     (pad - scrollY) * scale,
	}
}

// RawTextLineVisible takes slotTop in pane-local px, y down, against the
// visible band [0, h).
func RawTextLineVisible(slotTop, slot, h float64) bool {
	return slotTop+slot > 0 && slotTop < h
}
