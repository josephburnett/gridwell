package markdown

import (
	"math"
	"strings"
	"unicode/utf8"
)

// caret.go maps between a source byte offset and a screen position over the
// laid-out draw ops — the geometry behind the rendered-mode text caret. Pure
// (it takes a Measure, like the layout pass) so click-to-place and
// caret-rendering can be unit-tested without a canvas. Only verbatim text runs
// (DrawOp.SrcLen > 0) participate; opaque runs are skipped.
//
// The mapping must stay *total* over the caret's domain. A renderer collapses
// or drops parts of the source — trailing spaces, the newline you just typed,
// the blank line between paragraphs — so those bytes belong to no run. But
// editing puts the caret on exactly those offsets all the time (type a space
// or hit Enter at the end of a line). When an offset lands in such a gap,
// PointFromCaret walks the literal source text of the gap instead of giving
// up. Two rules keep the walk honest to what was actually painted:
//
//   - Only whitespace advances. Markdown markers the renderer consumed (the
//     ** of bold, a heading's #, a link's brackets) have no glyphs, so they
//     contribute no width — a caret next to them sits at the visible text's
//     edge, not a phantom marker-width away.
//   - Newlines drop at most one rendered break. In flowing text markdown
//     collapses any run of blank lines into a single paragraph break, so two
//     or more newlines drop the caret exactly one line height plus one block
//     gap — where the next paragraph really renders. Inside a code block
//     every newline is a real line, so there each one steps a full line.
//
// The caret's *editable domain* is the set of offsets these rules can place
// faithfully: any offset inside a verbatim run, plus any offset separated
// from the preceding run by whitespace only. NextCaretStop/PrevCaretStop walk
// that domain; offsets inside consumed markers are skipped, both so the caret
// never appears in a position the renderer can't show and so ordinary arrow
// movement can't lead typing into the middle of a marker.

// PointFromCaret returns the logical-pixel position of a caret sitting at
// source byte `offset`. Inside a run: the run's left edge plus the measured
// width of the text before the offset. In a gap past a run: the nearest
// preceding run's position advanced across the gap per the package rules
// above. Before any run (an empty document, or nothing but whitespace so
// far): the document origin (style.PadX, 0), where the first typed glyph will
// render. y is the line's top and fontPx its size (the painter turns these
// into a vertical bar). ok is false only when non-whitespace source the
// renderer consumed precedes the offset with no run in between (e.g. an
// offset inside a leading "# " marker) — a position with no faithful point.
func PointFromCaret(ops []DrawOp, src string, offset int, style LayoutStyle, m Measure) (x, y, fontPx float64, ok bool) {
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
		// No run precedes the offset. If everything before it is whitespace
		// the renderer collapses it all: the caret (and the next glyph typed)
		// sits at the document origin, advanced only by the spaces after the
		// last newline. Anything else (a consumed marker) has no position.
		before := src[:offset]
		if strings.TrimLeft(before, " \t\n") != "" {
			return 0, 0, 0, false
		}
		tail := before[strings.LastIndexByte(before, '\n')+1:]
		return style.PadX + m(tail, style.BaseFontPx, StyleNone, false), 0, style.BaseFontPx, true
	}
	// The offset is past aOp's end, in source the render collapsed or consumed.
	// Advance across the gap: whitespace widens / drops lines, markers don't.
	gap := src[aEnd:offset]
	if nl := strings.Count(gap, "\n"); nl > 0 {
		tail := whitespaceIn(gap[strings.LastIndexByte(gap, '\n')+1:])
		lineH := aOp.FontPx * style.LineSpacing
		dy := float64(nl) * lineH
		if nl >= 2 && !aOp.CodeBlock {
			// Flowing text renders any blank-line run as ONE paragraph break;
			// the caret lands where the next paragraph's first glyph will.
			dy = lineH + style.BlockGap
		}
		return lineLeftX(ops, aOp.Y) + m(tail, aOp.FontPx, aOp.Style, aOp.Mono),
			aOp.Y + dy, aOp.FontPx, true
	}
	right := aOp.X + m(aOp.Text, aOp.FontPx, aOp.Style, aOp.Mono)
	return right + m(whitespaceIn(gap), aOp.FontPx, aOp.Style, aOp.Mono), aOp.Y, aOp.FontPx, true
}

// whitespaceIn returns only the space/tab bytes of s, in order — the part of
// a gap that has rendered width. Consumed markdown markers are dropped so
// they never contribute phantom advance.
func whitespaceIn(s string) string {
	if strings.IndexFunc(s, func(r rune) bool { return r != ' ' && r != '\t' }) < 0 {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' {
			b.WriteByte(s[i])
		}
	}
	return b.String()
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

// IsCaretStop reports whether offset is in the rendered caret's editable
// domain (see the package note): on a rune boundary and either inside a
// verbatim run or separated from the preceding run (or the document start)
// by whitespace only. Offsets inside consumed markers are not stops.
func IsCaretStop(ops []DrawOp, src string, offset int) bool {
	if offset < 0 || offset > len(src) {
		return false
	}
	if offset < len(src) && !utf8.RuneStart(src[offset]) {
		return false
	}
	aEnd := 0 // nearest preceding run end; document start when there is none
	for _, op := range ops {
		if op.Kind != OpText || op.SrcLen == 0 {
			continue
		}
		if offset >= op.SrcStart && offset <= op.SrcStart+op.SrcLen {
			return true
		}
		if e := op.SrcStart + op.SrcLen; e <= offset && e > aEnd {
			aEnd = e
		}
	}
	return strings.TrimLeft(src[aEnd:offset], " \t\n") == ""
}

// NextCaretStop returns the nearest caret stop strictly after offset, or
// offset unchanged when none exists (end of the editable domain).
func NextCaretStop(ops []DrawOp, src string, offset int) int {
	for o := offset + 1; o <= len(src); o++ {
		if IsCaretStop(ops, src, o) {
			return o
		}
	}
	return offset
}

// PrevCaretStop returns the nearest caret stop strictly before offset, or
// offset unchanged when none exists.
func PrevCaretStop(ops []DrawOp, src string, offset int) int {
	for o := offset - 1; o >= 0; o-- {
		if IsCaretStop(ops, src, o) {
			return o
		}
	}
	return offset
}

// CaretFromPoint returns the source byte offset nearest the point (px, py): the
// text run nearest the point (vertical line first, then horizontal), then the
// rune boundary within it whose x is closest to px. ok is false when there is no
// text run to land on.
//
// A click to the right of the last glyph on a line lands in whitespace the
// renderer dropped (the trailing spaces of a source line), which carries no run.
// To stay the inverse of PointFromCaret there, when the click is past a run's
// right edge and the source after it is whitespace up to a newline (or EOF), the
// caret goes to the end of that line — so clicking past the text then typing
// extends the line, rather than snapping back to the last glyph. src is the doc
// the ops were laid out from.
func CaretFromPoint(ops []DrawOp, src string, px, py float64, m Measure) (offset int, ok bool) {
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
	// Past the run's right edge: if the source that follows is dropped trailing
	// whitespace ending the line, land at the line's end (its last typed column).
	if right := op.X + m(op.Text, op.FontPx, op.Style, op.Mono); px > right {
		end := op.SrcStart + op.SrcLen
		i := end
		for i < len(src) && (src[i] == ' ' || src[i] == '\t') {
			i++
		}
		if i > end && (i == len(src) || src[i] == '\n') {
			return i, true
		}
	}
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
