package markdown

import (
	"math"
	"testing"
)

func almost(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestCoverScale(t *testing.T) {
	cases := []struct {
		name             string
		w, h, boxW, boxH float64
		want             float64
	}{
		{"wider dest -> width binds", 200, 100, 100, 100, 2},
		{"taller dest -> height binds", 100, 200, 100, 100, 2},
		{"square 1:1", 100, 100, 50, 50, 2},
		{"degenerate width -> 0", 100, 100, 0, 50, 0},
		{"degenerate height -> 0", 100, 100, 50, 0, 0},
		{"negative box -> 0", 100, 100, -5, 50, 0},
	}
	for _, c := range cases {
		if got := CoverScale(c.w, c.h, c.boxW, c.boxH); !almost(got, c.want) {
			t.Errorf("%s: CoverScale=%v want %v", c.name, got, c.want)
		}
	}
}

func TestPreviewScaleScroll(t *testing.T) {
	const natural, fixed, minS = 400.0, 1.0, 0.02

	// Focused: cover-crop the inner box, scroll from the live pane.
	f := PreviewScaleScroll(200, 100, true, 100, 100, 7, 9, 0, 0, 0, 0, natural, fixed, minS)
	if !almost(f.Scale, 2) || f.ScrollX != 7 || f.ScrollY != 9 {
		t.Errorf("focused = %+v, want scale 2 scroll (7,9)", f)
	}
	// Focused but degenerate inner box -> fixed scale, scroll still honored.
	f = PreviewScaleScroll(200, 100, true, 0, 0, 7, 9, 0, 0, 0, 0, natural, fixed, minS)
	if !almost(f.Scale, fixed) || f.ScrollX != 7 || f.ScrollY != 9 {
		t.Errorf("focused degenerate = %+v, want scale %v scroll (7,9)", f, fixed)
	}
	// Stored framing (not focused): cover-crop the saved window + saved scroll.
	f = PreviewScaleScroll(200, 100, false, 0, 0, 0, 0, 100, 50, 3, 4, natural, fixed, minS)
	if !almost(f.Scale, 2) || f.ScrollX != 3 || f.ScrollY != 4 {
		t.Errorf("stored = %+v, want scale 2 scroll (3,4)", f)
	}
	// Stored takes the larger of the two ratios (cover).
	f = PreviewScaleScroll(100, 300, false, 0, 0, 0, 0, 100, 100, 0, 0, natural, fixed, minS)
	if !almost(f.Scale, 3) {
		t.Errorf("stored cover = %+v, want scale 3", f)
	}
	// Neither: natural-width fit.
	f = PreviewScaleScroll(200, 100, false, 0, 0, 0, 0, 0, 0, 0, 0, natural, fixed, minS)
	if !almost(f.Scale, 0.5) || f.ScrollX != 0 || f.ScrollY != 0 {
		t.Errorf("natural = %+v, want scale 0.5 scroll (0,0)", f)
	}
	// Neither, huge document: clamped to minScale (shows its "shape").
	f = PreviewScaleScroll(4, 100, false, 0, 0, 0, 0, 0, 0, 0, 0, natural, fixed, minS)
	if !almost(f.Scale, minS) {
		t.Errorf("clamped = %+v, want scale %v", f, minS)
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
		name           string
		slotTop, slot, h float64
		want           bool
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
