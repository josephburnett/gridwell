package markdown

import (
	"math"
	"testing"
)

func almost(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// A text preview renders at a constant scale: the type size never follows
// grid zoom or the stored framing, and the tile is a window that reveals more
// as it grows.
func TestPreviewWindowFrame(t *testing.T) {
	const fixed = 1.0

	// The scale is footprint-independent: the same frame scale at wildly
	// different tile sizes. Zoom changes the tile px, never the type size.
	for _, innerW := range []float64{40, 160, 640} {
		f := PreviewWindowFrame(innerW, fixed, 1.0, 0, 0)
		if !almost(f.Scale, fixed) {
			t.Errorf("innerW %v: Scale = %v, want the constant %v", innerW, f.Scale, fixed)
		}
		// The doc wraps to the tile: layout width is the inner width at
		// scale 1, so a bigger tile shows more at the same type size.
		if !almost(f.ContentW, innerW/fixed) {
			t.Errorf("innerW %v: ContentW = %v, want %v (wrap to the tile)", innerW, f.ContentW, innerW/fixed)
		}
	}

	// content_zoom is the one owner of "make the text big": it scales the
	// type and narrows the logical wrap width to match.
	f := PreviewWindowFrame(200, fixed, 2.0, 0, 0)
	if !almost(f.Scale, 2.0) || !almost(f.ContentW, 100) {
		t.Errorf("contentZoom 2: frame = %+v, want scale 2 contentW 100", f)
	}

	// The stored scroll places the window, so the preview keeps showing the
	// place you left.
	f = PreviewWindowFrame(200, fixed, 1.0, 3, 47)
	if f.ScrollX != 3 || f.ScrollY != 47 {
		t.Errorf("scroll = (%v, %v), want (3, 47)", f.ScrollX, f.ScrollY)
	}

	// A degenerate content zoom can't zero the scale.
	f = PreviewWindowFrame(200, fixed, 0, 0, 0)
	if !almost(f.Scale, fixed) {
		t.Errorf("zero zoom: Scale = %v, want fallback %v", f.Scale, fixed)
	}
}

// The level-of-detail gate: with the type size constant, a small tile shows
// the alt-text banner alone. Content paints only when at least one body line
// fits.
func TestPreviewContentVisible(t *testing.T) {
	if PreviewContentVisible(PreviewBodyLinePx-1, 1.0) {
		t.Error("below one line: content must not paint")
	}
	if !PreviewContentVisible(PreviewBodyLinePx, 1.0) {
		t.Error("exactly one line: content paints")
	}
	// The gate scales with content zoom: bigger type needs more room.
	if PreviewContentVisible(PreviewBodyLinePx, 2.0) {
		t.Error("zoomed type in one un-zoomed line of room: must not paint")
	}
}

// The preview frame is a pure function of the tile's own facts: inner width,
// content zoom, stored scroll. No other pane's geometry appears in the
// signature, so a sibling pane's width cannot leak into a preview and re-wrap
// it.
func TestPreviewFrameHasNoCrossPaneInputs(t *testing.T) {
	// Same tile facts → identical frame, whatever any other pane is doing.
	a := PreviewWindowFrame(160, 1.0, 1.5, 2, 9)
	b := PreviewWindowFrame(160, 1.0, 1.5, 2, 9)
	if a != b {
		t.Errorf("frame is not a pure function of the tile's facts: %+v vs %+v", a, b)
	}
}

func TestRawTextLineSlot(t *testing.T) {
	// fontPx 13, mul 1.35, scale 1, pad 8, scrollY 0, asc 12, desc 4.
	// slot = 13*1.35 = 17.55; baseline = (17.55-16)/2 + 12 = 12.775; top0 = 8.
	s := RawTextLineSlot(13, 1.35, 1, 8, 0, 12, 4)
	if !almost(s.Slot, 17.55) || !almost(s.Baseline, 12.775) || !almost(s.Top0, 8) {
		t.Errorf("slot = %+v", s)
	}
	// scale 2 doubles slot and top; scrollY shifts top0 up.
	s = RawTextLineSlot(13, 1.35, 2, 8, 3, 24, 8)
	if !almost(s.Slot, 35.1) || !almost(s.Top0, (8-3)*2) {
		t.Errorf("scaled slot = %+v, want Slot 35.1 Top0 10", s)
	}
}

func TestRawTextLineVisible(t *testing.T) {
	cases := []struct {
		name             string
		slotTop, slot, h float64
		want             bool
	}{
		{"fully visible", 10, 17, 100, true},
		{"just above top (bottom edge >0)", -10, 17, 100, true},
		{"fully above", -20, 17, 100, false},
		{"at bottom edge", 99, 17, 100, true},
		{"fully below", 100, 17, 100, false},
	}
	for _, c := range cases {
		if got := RawTextLineVisible(c.slotTop, c.slot, c.h); got != c.want {
			t.Errorf("%s: RawTextLineVisible(%v,%v,%v)=%v want %v", c.name, c.slotTop, c.slot, c.h, got, c.want)
		}
	}
}
