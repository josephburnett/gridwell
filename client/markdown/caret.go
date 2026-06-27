package markdown

import (
	"math"
	"strings"
)

// caret.go maps between a source byte offset and a screen position over the
// laid-out draw ops — the geometry behind the rendered-mode text caret. Pure
// (it takes a Measure, like the layout pass) so click-to-place and
// caret-rendering can be unit-tested without a canvas. Only verbatim text runs
// (DrawOp.SrcLen > 0) participate; opaque runs are skipped.
//
// The mapping must stay *total* over the source. A renderer collapses or drops
// parts of the source — trailing spaces, the newline you just typed, the blank
// line between paragraphs — so those bytes belong to no run. But editing puts
// the caret on exactly those offsets all the time (type a space or hit Enter at
// the end of a line). When an offset lands in such a gap, PointFromCaret walks
// the literal source text of the gap instead of giving up: whitespace widens
// the caret along the line, and each '\n' drops it to the next line. The caret
// then follows the buffer through whitespace the render doesn't show.

// PointFromCaret returns the logical-pixel position of a caret sitting at source
// byte `offset`. Inside a run: the run's left edge plus the measured width of
// the text before the offset. In a gap past a run: the nearest preceding run's
// position advanced across the gap's source text (see the package note above).
// y is the line's top and fontPx its size (the painter turns these into a
// vertical bar). lineSpacing scales fontPx into the line height used to step
// down per newline. ok is false only when no run precedes the offset (an empty
// document, or an offset before the first glyph).
func PointFromCaret(ops []DrawOp, src string, offset int, lineSpacing float64, m Measure) (x, y, fontPx float64, ok bool) {
	if offset < 0 {
		offset = 0
	}
	if offset > len(src) {
		offset = len(src)
	}
	var aOp DrawOp
	aEnd := -1 // end offset of the nearest run at or before the offset
	for _, op := range ops {
		if op.Kind != OpText || op.SrcLen == 0 {
			continue
		}
		start, end := op.SrcStart, op.SrcStart+op.SrcLen
		if offset >= start && offset <= end {
			bi := clampByte(op.Text, offset-start)
			return op.X + m(op.Text[:bi], op.FontPx, op.Style, op.Mono), op.Y, op.FontPx, true
		}
		if end <= offset && end > aEnd {
			aEnd, aOp = end, op
		}
	}
	if aEnd < 0 {
		return 0, 0, 0, false
	}
	// The offset is past aOp's end, in source the render collapsed. Advance
	// across the literal gap text: newlines drop to a new line at the line's
	// left margin; otherwise whitespace widens along the current line.
	gap := src[aEnd:offset]
	if nl := strings.Count(gap, "\n"); nl > 0 {
		tail := gap[strings.LastIndexByte(gap, '\n')+1:]
		lineH := aOp.FontPx * lineSpacing
		return lineLeftX(ops, aOp.Y) + m(tail, aOp.FontPx, aOp.Style, aOp.Mono),
			aOp.Y + float64(nl)*lineH, aOp.FontPx, true
	}
	right := aOp.X + m(aOp.Text, aOp.FontPx, aOp.Style, aOp.Mono)
	return right + m(gap, aOp.FontPx, aOp.Style, aOp.Mono), aOp.Y, aOp.FontPx, true
}

// lineLeftX is the left edge of the visual line whose top is at y: the smallest
// X among the text runs on that line. Used to place a caret that has dropped
// onto a new (often empty) line at the same left margin as the line above.
func lineLeftX(ops []DrawOp, y float64) float64 {
	lx := math.Inf(1)
	for _, op := range ops {
		if op.Kind == OpText && op.Y == y && op.X < lx {
			lx = op.X
		}
	}
	if math.IsInf(lx, 1) {
		return 0
	}
	return lx
}

// CaretBar returns the top y and height of the caret bar for a run whose top is
// at y with font size fontPx. Text is painted with a "top" baseline, so the
// glyph em box is [y, y+fontPx]; the bar grows that box by a small symmetric
// margin so it reads as centered on the text rather than hanging below it.
func CaretBar(y, fontPx float64) (top, height float64) {
	const overhang = 0.1
	return y - fontPx*overhang, fontPx * (1 + 2*overhang)
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
