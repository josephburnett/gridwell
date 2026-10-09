// Package wsbar owns the bottom bar's geometry: the circle slot and the nav
// chain. Render and input read the same segment rects, so the crumb you see is
// the crumb you hit. The band is reserved layout and the pane tree ends at its
// top edge, so nothing sized from a pane can paint over it; only the bar's own
// chrome moves with focus.
package wsbar

import "github.com/josephburnett/gridwell/client/pane"

// Band returns paneH as both the pane tree's height and the band's top edge,
// so layout and bar cannot disagree about where they meet. ok=false when what
// is left cannot hold the band.
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

// Rect is the one answer to where the bar is: render, hit-test and the rename
// inputs all read it. It is the band's row whichever pane has focus, so panes
// never resize as focus moves, and the focused pane's span across, clamped
// whole into the window. ok=false leaves the band plain background.
func Rect(winW, winH, stripH, paneX, paneW float64) (x, top, w float64, ok bool) {
	top, ok = Band(winH, stripH)
	if !ok || winW <= 0 || paneW <= 0 {
		return 0, 0, 0, false
	}
	w = paneW
	if w > winW {
		w = winW
	}
	x = paneX + (paneW-w)/2 // centered under the pane when the window clips it
	if x+w > winW {
		x = winW - w
	}
	if x < 0 {
		x = 0
	}
	return x, top, w, true
}

type Zone int

const (
	// ZoneOutside belongs to a pane or to the notice strip below.
	ZoneOutside Zone = iota
	// ZoneBand is the band's row off the bar. No pane is in the band, so a
	// point here is never passed to one; a left press focuses the column
	// above it (BandFocus).
	ZoneBand
	ZoneBar
)

// Where classifies a point against the rect Rect returned.
func Where(px, py, x, top, w float64) Zone {
	if py < top || py >= top+RowH {
		return ZoneOutside
	}
	if px < x || px >= x+w {
		return ZoneBand
	}
	return ZoneBar
}

// BandFocus is the pane a left press at x in the band beside the bar
// focuses: of the panes whose span across covers x, the one that held focus
// most recently in recency (pane.Tree.Recency), else the lowest, which sits
// on the band. ok=false when no pane covers x.
func BandFocus(rects map[string]pane.Rect, recency []string, x float64) (string, bool) {
	covers := func(r pane.Rect) bool { return x >= r.X && x < r.X+r.W }
	for _, id := range recency {
		if r, ok := rects[id]; ok && covers(r) {
			return id, true
		}
	}
	best, found := "", false
	for id, r := range rects {
		if !covers(r) {
			continue
		}
		if b := rects[best]; !found || r.Y > b.Y || (r.Y == b.Y && id < best) {
			best, found = id, true
		}
	}
	return best, found
}

// RowH keeps the band thin while a square crumb stays legible as a preview.
const RowH = 32.0

// SlotW is reserved at the bar's right end for the circle button. It sits in
// the band, so it never obscures content, and Layout never places a crumb in
// it.
const SlotW = 48.0

// Segment's Rect is relative to the bar's left edge and Index is the position
// in the caller's full list, so under left-truncation the visible segments
// still point at the right crumbs.
type Segment struct {
	Index int
	X, W  float64
}

// BoundaryW makes a pane-tile boundary crumb stand out from the RowH squares
// of chain crumbs. It is the rename target.
const BoundaryW = 120.0

// Layout takes widths[i] as RowH for a chain crumb and BoundaryW for a
// pane-tile boundary. When the band cannot fit them all, crumbs drop from the
// left and the survivors keep full size, a too-small preview reading as
// nothing. The current pane's name is a centered title, not a crumb.
func Layout(widths []float64, width float64) []Segment {
	if len(widths) == 0 || width <= 0 {
		return nil
	}
	width -= SlotW // the right-end circle slot is reserved
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

const titlePad = 8.0

const minTitleW = 24.0

// TitleInset is the room on each side of the title's text inside its span.
const TitleInset = 12.0

// TitleSpan centers the title between the crumbs' end and the circle slot, so
// growing crumbs cannot crowd it one-sidedly. textW is the text's own width;
// textX is where the text starts, so one too wide is clipped from the right
// and keeps its most specific part. x and textX are relative to the band's
// left edge and ok=false when less than minTitleW remains.
func TitleSpan(crumbsEnd, width, textW float64) (x, w, textX float64, ok bool) {
	left := crumbsEnd + titlePad
	right := width - SlotW - titlePad
	if right-left < minTitleW {
		return 0, 0, 0, false
	}
	w = min(textW+2*TitleInset, right-left)
	x = left + (right-left-w)/2
	return x, w, x + TitleInset, true
}

// CrumbsEnd is where the chain stops and the title's room begins, relative to
// the bar's left edge.
func CrumbsEnd(segs []Segment) float64 {
	if n := len(segs); n > 0 {
		return segs[n-1].X + segs[n-1].W
	}
	return 0
}

// At takes x relative to the bar's left edge.
func At(segs []Segment, x float64) (Segment, bool) {
	for _, s := range segs {
		if x >= s.X && x < s.X+s.W {
			return s, true
		}
	}
	return Segment{}, false
}

// SegmentAt returns the visible segment for a full-list index; ok=false when
// it was truncated away. The inline rename input is placed over it.
func SegmentAt(segs []Segment, index int) (Segment, bool) {
	for _, s := range segs {
		if s.Index == index {
			return s, true
		}
	}
	return Segment{}, false
}
