package markdown

import (
	"math"
	"testing"
)

// opByText finds the first OpText with the given text.
func opByText(r LayoutResult, text string) (DrawOp, bool) {
	for _, op := range r.Ops {
		if op.Kind == OpText && op.Text == text {
			return op, true
		}
	}
	return DrawOp{}, false
}

// caretX is PointFromCaret keeping only the x (the helpers below assert x).
func caretX(t *testing.T, r LayoutResult, src string, offset int) (float64, bool) {
	t.Helper()
	x, _, _, ok := PointFromCaret(r.Ops, src, offset, DefaultLayoutStyle().LineSpacing, monoMeasure)
	return x, ok
}

func TestPointFromCaretPlacesAlongRun(t *testing.T) {
	// "hello world": runes are fontPx*0.5 = 8px each (BaseFontPx 16), PadX 8.
	src := "hello world\n"
	r := layoutOf(t, src, 1000)
	hello, ok := opByText(r, "hello")
	if !ok {
		t.Fatal("no hello op")
	}
	cases := []struct {
		offset int
		wantX  float64
	}{
		{0, hello.X},         // before "h"
		{3, hello.X + 3*8},   // between "hel" and "lo"
		{5, hello.X + 5*8},   // end of "hello"
		{6, hello.X + 6*8},   // start of "world" (after the space)
		{11, hello.X + 11*8}, // end of "world"
	}
	for _, c := range cases {
		x, ok := caretX(t, r, src, c.offset)
		if !ok {
			t.Errorf("offset %d: not ok", c.offset)
			continue
		}
		if x != c.wantX {
			t.Errorf("offset %d: x = %v, want %v", c.offset, x, c.wantX)
		}
	}
}

// TestCaretFollowsCollapsedWhitespace is the regression for the editing bug:
// whitespace the renderer drops (a trailing space, the newline just typed) must
// still move the caret. Before the fix the caret stuck at the end of the last
// rendered run, so typing space/Enter advanced the source but not the cursor.
func TestCaretFollowsCollapsedWhitespace(t *testing.T) {
	// Trailing space: goldmark drops it, so no run carries offset 6. The caret
	// must still advance one space width past "hello".
	t.Run("trailing space", func(t *testing.T) {
		src := "hello "
		r := layoutOf(t, src, 1000)
		hello, _ := opByText(r, "hello")
		at5, _ := caretX(t, r, src, 5)
		at6, ok := caretX(t, r, src, 6)
		if !ok {
			t.Fatal("offset 6 not ok")
		}
		if at6 != hello.X+6*8 {
			t.Errorf("trailing-space caret x = %v, want %v", at6, hello.X+6*8)
		}
		if at6 <= at5 {
			t.Errorf("caret did not advance across the trailing space (%v -> %v)", at5, at6)
		}
	})
	// Trailing newline: the caret drops to the next line at the left margin.
	t.Run("trailing newline", func(t *testing.T) {
		src := "hello\n"
		r := layoutOf(t, src, 1000)
		hello, _ := opByText(r, "hello")
		x, y, _, ok := PointFromCaret(r.Ops, src, 6, DefaultLayoutStyle().LineSpacing, monoMeasure)
		if !ok {
			t.Fatal("offset 6 not ok")
		}
		if x != hello.X {
			t.Errorf("new-line caret x = %v, want left margin %v", x, hello.X)
		}
		wantY := hello.Y + hello.FontPx*DefaultLayoutStyle().LineSpacing
		if y != wantY {
			t.Errorf("new-line caret y = %v, want %v (one line down)", y, wantY)
		}
	})
	// Several blank lines: each newline steps down one line height.
	t.Run("multiple newlines", func(t *testing.T) {
		src := "hi\n\n\n"
		r := layoutOf(t, src, 1000)
		hi, _ := opByText(r, "hi")
		lh := hi.FontPx * DefaultLayoutStyle().LineSpacing
		for n := 1; n <= 3; n++ {
			_, y, _, ok := PointFromCaret(r.Ops, src, 2+n, DefaultLayoutStyle().LineSpacing, monoMeasure)
			if !ok {
				t.Fatalf("offset %d not ok", 2+n)
			}
			if y != hi.Y+float64(n)*lh {
				t.Errorf("offset %d: y = %v, want %v", 2+n, y, hi.Y+float64(n)*lh)
			}
		}
	})
}

// TestCaretBarCentersOnGlyphBox: the bar straddles the em box [y, y+fontPx]
// symmetrically, so it sits on the text rather than hanging below it.
func TestCaretBarCentersOnGlyphBox(t *testing.T) {
	const y, fontPx = 100.0, 16.0
	top, h := CaretBar(y, fontPx)
	overTop := y - top                  // margin above the em box
	overBot := (top + h) - (y + fontPx) // margin below the em box
	if overTop <= 0 || overBot <= 0 {
		t.Fatalf("bar must straddle the em box: top=%v h=%v (over %v/%v)", top, h, overTop, overBot)
	}
	if math.Abs(overTop-overBot) > 1e-9 {
		t.Errorf("bar not centered: %v above vs %v below the em box", overTop, overBot)
	}
}

func TestCaretFromPointFindsOffset(t *testing.T) {
	src := "hello world\n"
	r := layoutOf(t, src, 1000)
	hello, _ := opByText(r, "hello")
	y := hello.Y + 1 // a hair below the run's top, inside the band
	cases := []struct {
		px   float64
		want int
	}{
		{hello.X, 0},         // far left → before "h"
		{hello.X + 3*8, 3},   // inside "hello"
		{hello.X + 5*8, 5},   // end of "hello" / start of space
		{hello.X + 6*8, 6},   // start of "world"
		{hello.X + 11*8, 11}, // end of "world"
		{hello.X + 1000, 11}, // far right, no trailing space → last boundary (11)
	}
	for _, c := range cases {
		got, ok := CaretFromPoint(r.Ops, src, c.px, y, monoMeasure)
		if !ok {
			t.Errorf("px %v: not ok", c.px)
			continue
		}
		if got != c.want {
			t.Errorf("px %v: offset = %d, want %d", c.px, got, c.want)
		}
	}
}

// TestCaretFromPointTrailingWhitespace: a click past the last glyph on a line
// whose source has trailing whitespace the renderer dropped lands at the line's
// end (so click-past-text then type extends the line) — the inverse of
// PointFromCaret following that same dropped whitespace.
func TestCaretFromPointTrailingWhitespace(t *testing.T) {
	src := "hello   \nx" // 3 trailing spaces goldmark drops; then a 2nd line
	r := layoutOf(t, src, 1000)
	hello, ok := opByText(r, "hello")
	if !ok {
		t.Fatal("no hello op")
	}
	// Click far to the right on hello's line → end of "hello   " (offset 8),
	// not clamped back to the end of the glyph "hello" (offset 5).
	got, ok := CaretFromPoint(r.Ops, src, hello.X+1000, hello.Y+1, monoMeasure)
	if !ok {
		t.Fatal("not ok")
	}
	if got != 8 {
		t.Errorf("trailing-space click: offset = %d, want 8 (end of line)", got)
	}
	// And it round-trips: PointFromCaret(8) sits to the right of "hello".
	x, _, _, _ := PointFromCaret(r.Ops, src, 8, DefaultLayoutStyle().LineSpacing, monoMeasure)
	if x <= hello.X+5*8 {
		t.Errorf("PointFromCaret(8) x = %v, want right of hello (%v)", x, hello.X+5*8)
	}
}

// TestCaretRoundTrips: placing a caret at an offset then hit-testing that exact
// point returns the same offset, for every rune boundary of a multi-line doc.
func TestCaretRoundTrips(t *testing.T) {
	src := "# Title\n\nfirst line\nsecond line\n"
	ls := DefaultLayoutStyle().LineSpacing
	r := layoutOf(t, src, 1000)
	for _, op := range opsOfKind(r, OpText) {
		if op.SrcLen == 0 {
			continue
		}
		for _, b := range runeBoundaries(op.Text) {
			offset := op.SrcStart + b
			x, y, fp, ok := PointFromCaret(r.Ops, src, offset, ls, monoMeasure)
			if !ok {
				t.Fatalf("offset %d (run %q): PointFromCaret not ok", offset, op.Text)
			}
			got, ok := CaretFromPoint(r.Ops, src, x, y+fp/2, monoMeasure)
			if !ok {
				t.Fatalf("offset %d: CaretFromPoint not ok", offset)
			}
			// A boundary shared by two runs can resolve to either side's equal
			// pixel position; assert the round-tripped point matches, not the
			// raw offset.
			x2, _, _, _ := PointFromCaret(r.Ops, src, got, ls, monoMeasure)
			if x2 != x {
				t.Errorf("run %q offset %d → point %v → offset %d → point %v",
					op.Text, offset, x, got, x2)
			}
		}
	}
}

func TestPointFromCaretEmptyOps(t *testing.T) {
	if _, _, _, ok := PointFromCaret(nil, "", 0, 1.4, monoMeasure); ok {
		t.Error("empty ops should report ok=false")
	}
	if _, ok := CaretFromPoint(nil, "", 0, 0, monoMeasure); ok {
		t.Error("empty ops should report ok=false")
	}
}
