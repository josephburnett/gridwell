// Package wsbar owns the bottom bar's geometry: the always-reserved band at
// the bottom of the window that carries the workspace crumbs (tmux-style
// named rectangles, outermost first), the focused pane's descent chain
// (square tile previews, root inclusive — issue #212), and their ascent
// gestures. Pure Go (no js): render and input read the SAME segment rects,
// so the crumb you see is exactly the crumb you hit — the errsurface
// strip's reserved-band pattern, applied again.
//
// The band is always present (owner decision 2026-07-30, issue #212): the
// bar is the one home for "where am I", so it never comes and goes.
// Height() is subtracted inside rootLayoutRect, so panes and native views
// can never paint over the bar, exactly as with the error strip.
package wsbar

// RowH is the bar's height in CSS px. 32 keeps the band thin while a
// square chain crumb (side RowH) stays legible as a preview.
const RowH = 32.0

// maxCrumbW keeps a single workspace crumb from swallowing the bar — and
// since the bar lives inside the ACTIVE pane (issue #220), space is tight:
// the crumb is deliberately narrow now (owner request).
const maxCrumbW = 120.0

// AnchorW is the small solid block at the chain's left when NOT inside a
// workspace (issue #220): the same pane-tile teal, no name, roughly a
// third as wide as the bar is tall — it anchors the cookies.
const AnchorW = RowH / 3

// SlotW is the width reserved at the bar's RIGHT end for the circle
// button slot (issue #214): the + menu / back / refresh / ascend handle,
// moved off the pane corner so it never obscures content and needs no
// native overlay over live views. Layout never places a crumb inside it.
const SlotW = 48.0

// Height returns the band height to reserve. Always RowH: the bar is
// permanent chrome now, not workspace-only.
func Height() float64 { return RowH }

// Kind distinguishes the two crumb families sharing the bar.
type Kind int

const (
	// KindWorkspace is a named workspace rectangle; Index is the 1-based
	// stack level, leftmost (outermost workspace) = 1.
	KindWorkspace Kind = iota
	// KindChain is a square descent-chain preview; Index is the 0-based
	// index into the focused pane's DescentChain.
	KindChain
	// KindAnchor is the nameless teal block that fronts the chain outside
	// a workspace (issue #220). Index is always 0; it owns no gesture.
	KindAnchor
)

// Segment is one crumb's hit/draw rect, positioned relative to the bar's
// left edge (the caller translates by the bar origin).
type Segment struct {
	Kind  Kind
	Index int
	X, W  float64
}

// Layout lays the bar left-to-right: wsCount workspace crumbs (even
// division capped at maxCrumbW), then chainCount chain crumbs — squares of
// side RowH. (The current pane's name is a separate CENTERED title, not a
// crumb extension.) When the width can't fit everything, the chain squares
// shrink evenly (never the workspace crumbs — their labels are the harder
// thing to lose) so the whole location always stays visible and clickable.
func Layout(wsCount, chainCount int, width float64) []Segment {
	if wsCount < 0 {
		wsCount = 0
	}
	if chainCount < 0 {
		chainCount = 0
	}
	if wsCount+chainCount == 0 || width <= 0 {
		return nil
	}
	width -= SlotW // the right-end circle slot is reserved (issue #214)
	if width <= 0 {
		return nil
	}
	square := RowH
	wsW := 0.0
	if wsCount > 0 {
		wsW = (width - float64(chainCount)*square) / float64(wsCount)
		if wsW > maxCrumbW {
			wsW = maxCrumbW
		}
		if wsW < 0 {
			wsW = 0
		}
	}
	if chainCount > 0 {
		avail := width - float64(wsCount)*wsW
		if wsCount == 0 {
			avail -= AnchorW
		}
		if float64(chainCount)*square > avail {
			square = avail / float64(chainCount)
			if square < 0 {
				square = 0
			}
		}
	}
	out := make([]Segment, 0, wsCount+chainCount+1)
	x := 0.0
	if wsCount == 0 {
		// The anchor block fronts the cookies when no workspace crumb does.
		out = append(out, Segment{Kind: KindAnchor, X: x, W: AnchorW})
		x += AnchorW
	}
	for i := 0; i < wsCount; i++ {
		out = append(out, Segment{Kind: KindWorkspace, Index: i + 1, X: x, W: wsW})
		x += wsW
	}
	for i := 0; i < chainCount; i++ {
		out = append(out, Segment{Kind: KindChain, Index: i, X: x, W: square})
		x += square
	}
	return out
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

// WorkspaceSegment returns the rect of the workspace crumb at the given
// 1-based level, or ok=false. Used to place the inline rename input over
// the exact crumb the renderer drew.
func WorkspaceSegment(segs []Segment, level int) (Segment, bool) {
	for _, s := range segs {
		if s.Kind == KindWorkspace && s.Index == level {
			return s, true
		}
	}
	return Segment{}, false
}
