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
	x, _, _, ok := PointFromCaret(r.Ops, src, offset, DefaultLayoutStyle(), monoMeasure)
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
		x, y, _, ok := PointFromCaret(r.Ops, src, 6, DefaultLayoutStyle(), monoMeasure)
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
	// Blank-line runs in flowing text collapse to ONE paragraph break: past the
	// first newline the caret sits where the next paragraph's glyphs will
	// render (one line height + one block gap), no matter how many newlines
	// the source holds. Before the fix each newline stepped another line, so
	// repeated Enter sank the caret with no rendered change to match.
	t.Run("multiple newlines collapse to one paragraph break", func(t *testing.T) {
		src := "hi\n\n\n"
		st := DefaultLayoutStyle()
		r := layoutOf(t, src, 1000)
		hi, _ := opByText(r, "hi")
		lh := hi.FontPx * st.LineSpacing
		wantY := []float64{hi.Y + lh, hi.Y + lh + st.BlockGap, hi.Y + lh + st.BlockGap}
		for n := 1; n <= 3; n++ {
			_, y, _, ok := PointFromCaret(r.Ops, src, 2+n, st, monoMeasure)
			if !ok {
				t.Fatalf("offset %d not ok", 2+n)
			}
			if y != wantY[n-1] {
				t.Errorf("offset %d: y = %v, want %v", 2+n, y, wantY[n-1])
			}
		}
	})
	// Inside a code block every newline is a real rendered line, so the caret
	// steps a full line height per newline — blank code lines included.
	t.Run("code block newlines step per line", func(t *testing.T) {
		src := "```\nab\n\n\ncd\n```\n"
		st := DefaultLayoutStyle()
		r := layoutOf(t, src, 1000)
		ab, ok := opByText(r, "ab")
		if !ok || !ab.CodeBlock {
			t.Fatalf("no code-block run for ab (op=%+v)", ab)
		}
		lh := ab.FontPx * st.LineSpacing
		// Offsets 7, 8, 9: after ab's newline, then each blank line.
		for n := 1; n <= 3; n++ {
			_, y, _, ok := PointFromCaret(r.Ops, src, 6+n, st, monoMeasure)
			if !ok {
				t.Fatalf("offset %d not ok", 6+n)
			}
			if y != ab.Y+float64(n)*lh {
				t.Errorf("offset %d: y = %v, want %v", 6+n, y, ab.Y+float64(n)*lh)
			}
		}
		// And "cd" really renders on that third line — the caret walk agrees
		// with the layout.
		cd, _ := opByText(r, "cd")
		if cd.Y != ab.Y+3*lh {
			t.Errorf("cd renders at y=%v, want %v", cd.Y, ab.Y+3*lh)
		}
	})
}

// TestCaretIgnoresConsumedMarkers: markdown markers the renderer consumed
// (the ** of bold) have no glyphs, so a caret adjacent to them sits flush at
// the visible text — no phantom marker-width drift.
func TestCaretIgnoresConsumedMarkers(t *testing.T) {
	src := "x **bold** y\n"
	r := layoutOf(t, src, 1000)
	bold, ok := opByText(r, "bold")
	if !ok {
		t.Fatal("no bold op")
	}
	// Offset 2 (end of "x "), 3 (inside the markers), 4 (start of "bold") are
	// all the same visible position: the left edge of "bold".
	for _, off := range []int{2, 3, 4} {
		x, ok := caretX(t, r, src, off)
		if !ok {
			t.Fatalf("offset %d not ok", off)
		}
		if x != bold.X {
			t.Errorf("offset %d: x = %v, want %v (no phantom marker width)", off, x, bold.X)
		}
	}
}

// TestCaretEmptyDocument: an empty (or whitespace-only) document still has a
// caret — at the document origin, where the first typed glyph will render.
// Before the fix ok was false, so a fresh doc showed no caret at all.
func TestCaretEmptyDocument(t *testing.T) {
	st := DefaultLayoutStyle()
	x, y, fp, ok := PointFromCaret(nil, "", 0, st, monoMeasure)
	if !ok {
		t.Fatal("empty doc: not ok")
	}
	if x != st.PadX || y != 0 || fp != st.BaseFontPx {
		t.Errorf("empty doc caret = (%v,%v,%v), want (%v,0,%v)", x, y, fp, st.PadX, st.BaseFontPx)
	}
	// Whitespace-only source: leading blank lines collapse entirely — typing
	// renders at the top — so the caret stays at the origin, advanced only by
	// spaces after the last newline.
	src := "\n\n  "
	x, y, _, ok = PointFromCaret(nil, src, len(src), st, monoMeasure)
	if !ok {
		t.Fatal("whitespace-only doc: not ok")
	}
	if y != 0 || x != st.PadX+monoMeasure("  ", st.BaseFontPx, StyleNone, false) {
		t.Errorf("whitespace-only caret = (%v,%v), want origin + two spaces", x, y)
	}
}

// TestCaretStops: arrow movement walks exactly the editable domain — through
// text and typed whitespace, over consumed markers.
func TestCaretStops(t *testing.T) {
	src := "x **bold** y\n"
	r := layoutOf(t, src, 1000)
	// Forward from the start: every rune of "x ", then a jump over the
	// opening marker to "bold", over the closing marker to " y".
	want := []int{1, 2, 4, 5, 6, 7, 8, 10, 11, 12, 13}
	off := 0
	for i, w := range want {
		off = NextCaretStop(r.Ops, src, off)
		if off != w {
			t.Fatalf("step %d: NextCaretStop = %d, want %d", i, off, w)
		}
	}
	if next := NextCaretStop(r.Ops, src, off); next != off {
		t.Errorf("NextCaretStop at end moved to %d", next)
	}
	// And the reverse walk visits the same stops.
	for i := len(want) - 2; i >= 0; i-- {
		off = PrevCaretStop(r.Ops, src, off)
		if off != want[i] {
			t.Fatalf("reverse: PrevCaretStop = %d, want %d", off, want[i])
		}
	}
	if PrevCaretStop(r.Ops, src, off) != 0 {
		t.Errorf("PrevCaretStop from %d should reach 0", off)
	}
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
	x, _, _, _ := PointFromCaret(r.Ops, src, 8, DefaultLayoutStyle(), monoMeasure)
	if x <= hello.X+5*8 {
		t.Errorf("PointFromCaret(8) x = %v, want right of hello (%v)", x, hello.X+5*8)
	}
}

// TestCaretRoundTrips: placing a caret at an offset then hit-testing that exact
// point returns the same offset, for every rune boundary of a multi-line doc.
func TestCaretRoundTrips(t *testing.T) {
	src := "# Title\n\nfirst line\nsecond line\n"
	ls := DefaultLayoutStyle()
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

func TestPointFromCaretUnmappableOffset(t *testing.T) {
	// An offset preceded by consumed non-whitespace with no run in between
	// (inside a leading "# " marker) has no faithful position.
	src := "# Title\n"
	r := layoutOf(t, src, 1000)
	if _, _, _, ok := PointFromCaret(r.Ops, src, 1, DefaultLayoutStyle(), monoMeasure); ok {
		t.Error("offset inside a leading marker should report ok=false")
	}
	if _, ok := CaretFromPoint(nil, "", 0, 0, monoMeasure); ok {
		t.Error("empty ops should report ok=false for CaretFromPoint")
	}
}
