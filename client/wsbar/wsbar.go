// Package wsbar owns the workspace bar's geometry: the reserved band at the
// bottom of the window that names the workspace nesting (tmux-style) and
// carries the workspace-ascent gesture. Pure Go (no js): render and input
// read the SAME segment rects, so the crumb you see is exactly the crumb you
// hit — the errsurface strip's reserved-band pattern, applied again.
//
// The bar exists only while the workspace stack is non-empty: the landing
// page / session tree has no bar (there is nothing to leave). Height() is
// subtracted inside rootLayoutRect, so panes and native views can never
// paint over the bar, exactly as with the error strip.
package wsbar

// RowH is the bar's height in CSS px when visible.
const RowH = 26.0

// maxCrumbW keeps a single crumb from swallowing the whole bar when the
// nesting is shallow; deep nesting divides the width evenly instead.
const maxCrumbW = 240.0

// Height returns the band height to reserve: 0 outside any workspace.
func Height(depth int) float64 {
	if depth == 0 {
		return 0
	}
	return RowH
}

// Segment is one crumb's hit/draw rect, positioned relative to the bar's
// left edge (the caller translates by the bar origin). Level is 1-based,
// leftmost (outermost workspace) = 1.
type Segment struct {
	Level int
	X, W  float64
}

// Segments lays the crumbs left-to-right across the bar width: even division
// capped at maxCrumbW. Labels are the caller's to draw (ellipsized into W).
func Segments(n int, width float64) []Segment {
	if n <= 0 || width <= 0 {
		return nil
	}
	w := width / float64(n)
	if w > maxCrumbW {
		w = maxCrumbW
	}
	out := make([]Segment, n)
	for i := range out {
		out[i] = Segment{Level: i + 1, X: float64(i) * w, W: w}
	}
	return out
}

// SegmentAt returns the crumb level at x (relative to the bar's left edge),
// or 0 when x falls outside every crumb.
func SegmentAt(segs []Segment, x float64) int {
	for _, s := range segs {
		if x >= s.X && x < s.X+s.W {
			return s.Level
		}
	}
	return 0
}
