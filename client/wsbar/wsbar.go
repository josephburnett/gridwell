// Package wsbar owns the bottom bar's geometry: the one band across the
// bottom of the window, carrying the nav chain — the complete path from the
// root as square tile previews, pane-tile boundaries included — and the
// circle slot. Pure Go: render and input read the same segment rects, so the
// crumb you see is exactly the crumb you hit.
//
// The band is always present: the bar is the one home for "where am I", so it
// never comes and goes. It is reserved layout — the pane tree ends at its top
// edge (Band) — so no pane, and no native surface sized from a pane, can
// paint over it.
package wsbar

// Band divides the window's vertical space: the pane tree gets everything
// above the band, the band is the RowH strip below it, and the caller's
// notice strip (stripH) keeps the bottom. paneH is the pane tree's height,
// which is also the band's top edge, since panes start at y=0 — one number,
// so the layout and the bar can never disagree about where they meet.
// ok=false when what is left cannot hold the band; then the panes take it
// all and no band is drawn.
func Band(winH, stripH float64) (paneH float64, ok bool) {
	avail := winH - stripH
	if avail < 0 {
		avail = 0
	}
	if avail < RowH {
		return avail, false
	}
	return avail - RowH, true
}

// RowH is the bar's height in CSS px. 32 keeps the band thin while a
// square chain crumb (side RowH) stays legible as a preview.
const RowH = 32.0

// SlotW is the width reserved at the bar's right end for the circle button
// slot: the + menu / back / refresh / ascend handle. It sits in the band so
// it never obscures content and needs no native overlay over live views.
// Layout never places a crumb inside it.
const SlotW = 48.0

// Segment is one crumb's hit/draw rect, positioned relative to the bar's
// left edge (the caller translates by the bar origin). Index is the position
// in the caller's full crumb list: under left-truncation the visible segments
// are a suffix, and Index keeps pointing at the right crumb.
type Segment struct {
	Index int
	X, W  float64
}

// BoundaryW is a pane-tile boundary crumb's width: the light-blue named bar.
// It stands out from the square previews, and the wide face is the rename
// target. Chain crumbs stay RowH squares.
const BoundaryW = 120.0

// Layout lays the one nav chain: the complete path from the root, left to
// right — RowH squares for chain crumbs, BoundaryW bars for pane-tile
// boundaries (widths[i] chooses per crumb; the caller passes RowH or
// BoundaryW). When the band can't fit them all, crumbs drop from the left:
// the tail — where you are — keeps priority, and survivors keep their full
// size, because a too-small preview reads as nothing. The current pane's
// name is a separate centered title, not a crumb.
func Layout(widths []float64, width float64) []Segment {
	if len(widths) == 0 || width <= 0 {
		return nil
	}
	width -= SlotW // the right-end circle slot is reserved
	// Walk from the tail, keeping crumbs while they fit.
	first := len(widths)
	rem := width
	for i := len(widths) - 1; i >= 0; i-- {
		if widths[i] > rem {
			break
		}
		rem -= widths[i]
		first = i
	}
	if first == len(widths) {
		return nil
	}
	out := make([]Segment, 0, len(widths)-first)
	x := 0.0
	for i := first; i < len(widths); i++ {
		out = append(out, Segment{Index: i, X: x, W: widths[i]})
		x += widths[i]
	}
	return out
}

// titlePad is the breathing room on either side of the centered title:
// off the last crumb on its left, off the circle slot on its right.
const titlePad = 8.0

// minTitleW is the narrowest span worth drawing a title into.
const minTitleW = 24.0

// TitleSpan places the centered pane title: centered in the free space
// between the crumbs' end and the circle slot, so growing crumbs cannot
// crowd it one-sidedly. crumbsEnd is the right edge of the last crumb (0
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
