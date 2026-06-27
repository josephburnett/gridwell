package markdown

import "math"

// caret.go maps between a source byte offset and a screen position over the
// laid-out draw ops — the geometry behind the rendered-mode text caret. Pure
// (it takes a Measure, like the layout pass) so click-to-place and
// caret-rendering can be unit-tested without a canvas. Only verbatim text runs
// (DrawOp.SrcLen > 0) participate; opaque runs are skipped.

// PointFromCaret returns the logical-pixel position of a caret sitting at source
// byte `offset`: the run's left edge plus the measured width of the text before
// the offset. y is the run's top and fontPx its size (the painter turns these
// into a vertical bar). When the offset falls in no run (e.g. past the last
// character, or in a gap), it falls back to the right edge of the latest run
// that ends at or before the offset. ok is false only when there is no text run
// at all.
func PointFromCaret(ops []DrawOp, offset int, m Measure) (x, y, fontPx float64, ok bool) {
	var fx, fy, ff float64
	fbEnd := -1 // best "before" fallback: end offset of the latest run <= offset
	for _, op := range ops {
		if op.Kind != OpText || op.SrcLen == 0 {
			continue
		}
		start, end := op.SrcStart, op.SrcStart+op.SrcLen
		if offset >= start && offset <= end {
			bi := clampByte(op.Text, offset-start)
			return op.X + m(op.Text[:bi], op.FontPx, op.Style, op.Mono), op.Y, op.FontPx, true
		}
		if end <= offset && end > fbEnd {
			fbEnd = end
			fx = op.X + m(op.Text, op.FontPx, op.Style, op.Mono)
			fy = op.Y
			ff = op.FontPx
		}
	}
	if fbEnd >= 0 {
		return fx, fy, ff, true
	}
	return 0, 0, 0, false
}

// CaretFromPoint returns the source byte offset nearest the point (px, py): the
// text run nearest the point (vertical line first, then horizontal), then the
// rune boundary within it whose x is closest to px. ok is false when there is no
// text run to land on.
func CaretFromPoint(ops []DrawOp, px, py float64, m Measure) (offset int, ok bool) {
	best := -1
	var bestScore float64
	for i, op := range ops {
		if op.Kind != OpText || op.SrcLen == 0 {
			continue
		}
		w := m(op.Text, op.FontPx, op.Style, op.Mono)
		// Vertical distance to the run's line band dominates so a click always
		// lands on the right line; horizontal distance breaks ties within it.
		vy := axisGap(py, op.Y, op.Y+op.FontPx)
		hx := axisGap(px, op.X, op.X+w)
		score := vy*1e6 + hx
		if best < 0 || score < bestScore {
			best, bestScore = i, score
		}
	}
	if best < 0 {
		return 0, false
	}
	op := ops[best]
	bestK, bestDx := 0, math.Inf(1)
	for _, b := range runeBoundaries(op.Text) {
		x := op.X + m(op.Text[:b], op.FontPx, op.Style, op.Mono)
		if d := math.Abs(px - x); d < bestDx {
			bestK, bestDx = b, d
		}
	}
	return op.SrcStart + bestK, true
}

// axisGap is the distance from v to the [lo, hi] interval (0 when inside).
func axisGap(v, lo, hi float64) float64 {
	switch {
	case v < lo:
		return lo - v
	case v > hi:
		return v - hi
	default:
		return 0
	}
}

// clampByte clamps a byte index into s to [0, len(s)].
func clampByte(s string, i int) int {
	if i < 0 {
		return 0
	}
	if i > len(s) {
		return len(s)
	}
	return i
}

// runeBoundaries returns the byte indices at which a caret may sit in s: 0,
// every rune start, and len(s). Iterating these (rather than every byte) keeps
// the caret on rune boundaries so multibyte text isn't split.
func runeBoundaries(s string) []int {
	out := []int{0}
	for i := range s {
		if i != 0 {
			out = append(out, i)
		}
	}
	return append(out, len(s))
}
