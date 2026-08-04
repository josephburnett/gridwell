// Package wsbar owns the bottom bar's geometry: the band at the bottom of
// the ACTIVE pane (issue #220) carrying the ONE nav chain (issue #245) —
// the complete path from the root as square tile previews, workspace
// boundaries (pane-tile crumbs) included — and the circle slot. Pure Go
// (no js): render and input read the SAME segment rects, so the crumb you
// see is exactly the crumb you hit — the errsurface strip's reserved-band
// pattern, applied again.
//
// The band is always present (owner decision 2026-07-30, issue #212): the
// bar is the one home for "where am I", so it never comes and goes. Native
// surfaces on the focused pane carve the band out of their rects
// (panebox.BarInset), so they can never paint over it.
package wsbar

// RowH is the bar's height in CSS px. 32 keeps the band thin while a
// square chain crumb (side RowH) stays legible as a preview.
const RowH = 32.0

// SlotW is the width reserved at the bar's RIGHT end for the circle
// button slot (issue #214): the + menu / back / refresh / ascend handle,
// moved off the pane corner so it never obscures content and needs no
// native overlay over live views. Layout never places a crumb inside it.
const SlotW = 48.0

// Segment is one crumb's hit/draw rect, positioned relative to the bar's
// left edge (the caller translates by the bar origin). Index is the
// position in the CALLER'S full crumb list — under left-truncation the
// visible segments are a suffix, and Index keeps pointing at the right
// crumb.
type Segment struct {
	Index int
	X, W  float64
}

// Layout lays the ONE nav chain (issue #245): chainCount square crumbs of
// side RowH, left to right — the complete path from the root, workspace
// boundaries included, laid out by the caller. When the band can't fit
// them all, crumbs drop from the LEFT: the tail — where you are — keeps
// priority, and the survivors keep their full size (no shrinking; a
// too-small preview reads as nothing). The current pane's name is a
// separate CENTERED title, not a crumb.
func Layout(chainCount int, width float64) []Segment {
	if chainCount <= 0 || width <= 0 {
		return nil
	}
	width -= SlotW // the right-end circle slot is reserved (issue #214)
	if width < RowH {
		return nil
	}
	visible := int(width / RowH)
	if visible > chainCount {
		visible = chainCount
	}
	first := chainCount - visible
	out := make([]Segment, 0, visible)
	x := 0.0
	for i := first; i < chainCount; i++ {
		out = append(out, Segment{Index: i, X: x, W: RowH})
		x += RowH
	}
	return out
}

// titlePad is the breathing room on either side of the centered title:
// off the last crumb on its left, off the circle slot on its right.
const titlePad = 8.0

// minTitleW is the narrowest span worth drawing a title into.
const minTitleW = 24.0

// TitleSpan places the centered pane title: centered in the free space
// BETWEEN the crumbs' end and the circle slot (issue #230 — centering in
// the whole band let growing crumbs crowd the title one-sidedly, and it
// never recentered). crumbsEnd is the right edge of the last crumb (0
// with none), width the band width, textW the measured title width
// (padding included). x is relative to the band's left edge; a title
// wider than the free space is clamped to it; ok=false when less than
// minTitleW remains.
func TitleSpan(crumbsEnd, width, textW float64) (x, w float64, ok bool) {
	left := crumbsEnd + titlePad
	right := width - SlotW - titlePad
	if right-left < minTitleW {
		return 0, 0, false
	}
	w = textW
	if w > right-left {
		w = right - left
	}
	return left + (right-left-w)/2, w, true
}

// At returns the segment under x (relative to the bar's left edge), or
// ok=false when x falls outside every crumb.
func At(segs []Segment, x float64) (Segment, bool) {
	for _, s := range segs {
		if x >= s.X && x < s.X+s.W {
			return s, true
		}
	}
	return Segment{}, false
}

// SegmentAt returns the visible segment for the given full-list index, or
// ok=false when it was truncated away. Used to place the inline rename
// input over the exact crumb the renderer drew.
func SegmentAt(segs []Segment, index int) (Segment, bool) {
	for _, s := range segs {
		if s.Index == index {
			return s, true
		}
	}
	return Segment{}, false
}
