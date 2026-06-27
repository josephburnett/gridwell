package markdown

import "testing"

// opByText finds the first OpText with the given text.
func opByText(r LayoutResult, text string) (DrawOp, bool) {
	for _, op := range r.Ops {
		if op.Kind == OpText && op.Text == text {
			return op, true
		}
	}
	return DrawOp{}, false
}

func TestPointFromCaretPlacesAlongRun(t *testing.T) {
	// "hello world": runes are fontPx*0.5 = 8px each (BaseFontPx 16), PadX 8.
	r := layoutOf(t, "hello world\n", 1000)
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
		{12, hello.X + 11*8}, // the trailing "\n" → falls back to end of "world"
	}
	for _, c := range cases {
		x, _, _, ok := PointFromCaret(r.Ops, c.offset, monoMeasure)
		if !ok {
			t.Errorf("offset %d: not ok", c.offset)
			continue
		}
		if x != c.wantX {
			t.Errorf("offset %d: x = %v, want %v", c.offset, x, c.wantX)
		}
	}
}

func TestCaretFromPointFindsOffset(t *testing.T) {
	r := layoutOf(t, "hello world\n", 1000)
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
		{hello.X + 1000, 11}, // far right → clamps to last boundary on the line
	}
	for _, c := range cases {
		got, ok := CaretFromPoint(r.Ops, c.px, y, monoMeasure)
		if !ok {
			t.Errorf("px %v: not ok", c.px)
			continue
		}
		if got != c.want {
			t.Errorf("px %v: offset = %d, want %d", c.px, got, c.want)
		}
	}
}

// TestCaretRoundTrips: placing a caret at an offset then hit-testing that exact
// point returns the same offset, for every rune boundary of a multi-line doc.
func TestCaretRoundTrips(t *testing.T) {
	r := layoutOf(t, "# Title\n\nfirst line\nsecond line\n", 1000)
	for _, op := range opsOfKind(r, OpText) {
		if op.SrcLen == 0 {
			continue
		}
		for _, b := range runeBoundaries(op.Text) {
			offset := op.SrcStart + b
			x, y, fp, ok := PointFromCaret(r.Ops, offset, monoMeasure)
			if !ok {
				t.Fatalf("offset %d (run %q): PointFromCaret not ok", offset, op.Text)
			}
			got, ok := CaretFromPoint(r.Ops, x, y+fp/2, monoMeasure)
			if !ok {
				t.Fatalf("offset %d: CaretFromPoint not ok", offset)
			}
			// A boundary shared by two runs can resolve to either side's equal
			// pixel position; assert the round-tripped point matches, not the
			// raw offset.
			x2, _, _, _ := PointFromCaret(r.Ops, got, monoMeasure)
			if x2 != x {
				t.Errorf("run %q offset %d → point %v → offset %d → point %v",
					op.Text, offset, x, got, x2)
			}
		}
	}
}

func TestPointFromCaretEmptyOps(t *testing.T) {
	if _, _, _, ok := PointFromCaret(nil, 0, monoMeasure); ok {
		t.Error("empty ops should report ok=false")
	}
	if _, ok := CaretFromPoint(nil, 0, 0, monoMeasure); ok {
		t.Error("empty ops should report ok=false")
	}
}
