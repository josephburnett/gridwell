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

	// Focused: cover-crop the inner box, scroll from the live pane. ContentW is
	// the inner width — the preview lays out at the SAME width the live pane did,
	// so it's a scaled copy, not a re-wrap.
	f := PreviewScaleScroll(200, 100, true, 100, 100, 7, 9, 0, 0, 0, 0, natural, fixed, minS)
	if !almost(f.Scale, 2) || f.ScrollX != 7 || f.ScrollY != 9 || !almost(f.ContentW, 100) {
		t.Errorf("focused = %+v, want scale 2 scroll (7,9) contentW 100", f)
	}
	// Focused but degenerate inner box -> fixed scale, natural width, scroll honored.
	f = PreviewScaleScroll(200, 100, true, 0, 0, 7, 9, 0, 0, 0, 0, natural, fixed, minS)
	if !almost(f.Scale, fixed) || f.ScrollX != 7 || f.ScrollY != 9 || !almost(f.ContentW, natural) {
		t.Errorf("focused degenerate = %+v, want scale %v scroll (7,9) contentW %v", f, fixed, natural)
	}
	// Stored framing (not focused): cover-crop the saved window; ContentW is the
	// saved width, so the preview reflows exactly as it did at ascent.
	f = PreviewScaleScroll(200, 100, false, 0, 0, 0, 0, 100, 50, 3, 4, natural, fixed, minS)
	if !almost(f.Scale, 2) || f.ScrollX != 3 || f.ScrollY != 4 || !almost(f.ContentW, 100) {
		t.Errorf("stored = %+v, want scale 2 scroll (3,4) contentW 100", f)
	}
	// Stored takes the larger of the two ratios (cover).
	f = PreviewScaleScroll(100, 300, false, 0, 0, 0, 0, 100, 100, 0, 0, natural, fixed, minS)
	if !almost(f.Scale, 3) || !almost(f.ContentW, 100) {
		t.Errorf("stored cover = %+v, want scale 3 contentW 100", f)
	}
	// Neither: natural-width fit, laid out at the natural width.
	f = PreviewScaleScroll(200, 100, false, 0, 0, 0, 0, 0, 0, 0, 0, natural, fixed, minS)
	if !almost(f.Scale, 0.5) || f.ScrollX != 0 || f.ScrollY != 0 || !almost(f.ContentW, natural) {
		t.Errorf("natural = %+v, want scale 0.5 scroll (0,0) contentW %v", f, natural)
	}
	// Neither, huge document: clamped to minScale (shows its "shape").
	f = PreviewScaleScroll(4, 100, false, 0, 0, 0, 0, 0, 0, 0, 0, natural, fixed, minS)
	if !almost(f.Scale, minS) || !almost(f.ContentW, natural) {
		t.Errorf("clamped = %+v, want scale %v contentW %v", f, minS, natural)
	}
}

// The I8 invariant, stated precisely: for a FIXED framing, the preview's layout
// width (ContentW) must not depend on the preview tile's footprint (w, h) — the
// tile's size on the parent grid only changes the cover Scale. If ContentW
// tracked the footprint, a tile shown at a different size would RE-WRAP its
// markdown to its own width instead of showing a scaled copy of what was framed
// — exactly the "preview ≠ what I left" / "preview goes wonky" report. The doc
// must reflow only when re-descended into a real pane, never per preview size.
func TestPreviewContentWidthInvariantToFootprint(t *testing.T) {
	const natural, fixed, minS = 400.0, 1.0, 0.02

	// Stored framing (the unfocused preview / ascent-return case): the saved
	// window is 120 wide. Render it at three very different footprints.
	const storedW = 120
	footprints := [][2]float64{{60, 40}, {240, 160}, {37, 211}}
	for _, fp := range footprints {
		f := PreviewScaleScroll(fp[0], fp[1], false, 0, 0, 0, 0, storedW, 80, 0, 0, natural, fixed, minS)
		if !almost(f.ContentW, storedW) {
			t.Errorf("footprint %vx%v: ContentW = %v, want the framing width %v (no re-wrap)",
				fp[0], fp[1], f.ContentW, storedW)
		}
	}

	// Focused (a live mirror in another pane): ContentW tracks the focused pane's
	// inner width, again independent of the mirror's own footprint.
	const innerW = 95
	for _, fp := range footprints {
		f := PreviewScaleScroll(fp[0], fp[1], true, innerW, 70, 0, 0, 0, 0, 0, 0, natural, fixed, minS)
		if !almost(f.ContentW, innerW) {
			t.Errorf("focused footprint %vx%v: ContentW = %v, want inner width %v (no re-wrap)",
				fp[0], fp[1], f.ContentW, innerW)
		}
	}
}

// TestPreviewNotAffectedByFocusedPaneWidth is the structural guard for
// issue #35 Mechanism A: a text tile's preview layout width (ContentW) must
// not depend on the width of any OTHER pane that happens to be descended into
// the same tile. Before the fix, drawMarkdownNode called paneFocusedOnFile and
// fed the OTHER pane's innerW as focused=true to PreviewScaleScroll, so a
// sibling pane of different width would RE-WRAP the preview (wrong-size).
//
// The fix: drawMarkdownNode always passes focused=false. The test verifies that
// with focused=false and stored framing, ContentW is always the stored width
// regardless of what innerW is passed (which is what a sibling pane would have
// been feeding in the old code).
func TestPreviewNotAffectedByFocusedPaneWidth(t *testing.T) {
	const natural, fixed, minS = 400.0, 1.0, 0.02
	const storedW, storedH int64 = 200, 150

	// focused=false (the post-fix path): ContentW is always the stored width,
	// regardless of innerW (a sibling pane's geometry is irrelevant).
	siblingWidths := []float64{50, 100, 200, 300, 400, 800}
	for _, sw := range siblingWidths {
		f := PreviewScaleScroll(100, 80, false, sw, 60, 0, 0, storedW, storedH, 0, 0, natural, fixed, minS)
		if !almost(f.ContentW, float64(storedW)) {
			t.Errorf("sibling width %v, focused=false: ContentW=%v, want stored %v (no cross-pane re-wrap)",
				sw, f.ContentW, storedW)
		}
	}

	// Document the pre-fix bug: focused=true with a sibling's innerW caused the
	// preview to re-wrap at that width. Each sibling width produced a different
	// ContentW — the bug: tile T1's preview in pane B would reflow to pane A's width.
	for _, sw := range siblingWidths {
		f := PreviewScaleScroll(100, 80, true, sw, 60, 0, 0, storedW, storedH, 0, 0, natural, fixed, minS)
		if !almost(f.ContentW, sw) {
			t.Errorf("focused=true (pre-fix bug path): ContentW=%v, expected %v (sibling width dominated)",
				f.ContentW, sw)
		}
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
