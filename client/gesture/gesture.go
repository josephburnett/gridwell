// Package gesture holds the pure decision logic for the right-button
// gesture surface: classifying a right-button-down into a gesture Kind,
// and resolving the pure-math gestures on release into a concrete result.
//
// It owns only the parts that don't touch the DOM, the pane tree, or the
// store. The App computes the resolved facts (is the cursor over a tile?
// is there a divider on this side? is this a URL descent?) from its own
// lookups and hands them in as an Input; gesture.Classify encodes the
// priority ordering between those facts — the piece worth testing in
// isolation. On release, the gestures whose outcome is pure geometry
// (Split, Resize, URLRefresh, Ascend) resolve here; the gestures whose
// outcome is a drop resolution (TileCenter→clone, drop-on-+-button→delete)
// are the job of the (still-pending) drop unification and stay in the App
// for now.
package gesture

import "github.com/josephburnett/gridwell/client/pane"

// Kind is the classification of an in-flight right-button gesture, fixed
// at right-button-down and never changed mid-gesture. None means the
// right-down armed nothing (e.g. an empty region with no divider).
type Kind int

const (
	None Kind = iota
	// EmbedHint surfaces the chain-link glyph over a tile-embed inside a
	// rendered text descent. Drag and release both do nothing.
	EmbedHint
	// Ascend is armed on the corner +/refresh/back circle when the pane
	// has somewhere to ascend to. Release inside the circle ascends;
	// dragging out cancels.
	Ascend
	// TileCenter is the clone grab handle: armed in a tile's inner third.
	// Drag past the threshold clones; bare release is a no-op.
	TileCenter
	// TileResize is armed on a tile outside its center: rubber-band the
	// footprint from the diagonally-opposite pinned corner.
	TileResize
	// Swap exchanges the origin pane with the pane under the cursor at
	// release.
	Swap
	// Split splits the pane along the armed side at the release ratio.
	// Since 2026-07-26 (issue #203) the right button ALWAYS splits from a
	// border — over a divider or at a screen edge alike; resizing (and
	// closing, under pressure) is the LEFT button's job.
	Split
)

// Input is the set of resolved facts at right-button-down. Every field is
// computed by the App from its own lookups; Classify only orders them.
// The booleans are evaluated in priority order, so a field only matters
// when every higher-priority field is false.
type Input struct {
	// OverEmbed is true when the pane is a rendered text descent and the
	// cursor is over a tile-embed. Highest priority — the embed hint wins
	// over everything so the chain-link is always discoverable.
	OverEmbed bool

	// InGridView is true when the pane shows a grid (p.TextFocus == 0) —
	// tile gestures are only valid there. OverTile is true when the cursor
	// is over a tile; InTileCenter is true when it's in that tile's inner
	// third (the clone handle) rather than the resize ring.
	InGridView   bool
	OverTile     bool
	InTileCenter bool

	// Region is the pane sub-region under the cursor (resize band / split
	// edge / swap center).
	Region pane.Region
}

// Classify maps the resolved facts to a gesture Kind. The two switches
// mirror onRightDown exactly: the special targets (embed, tile) take
// priority in that order; only if none of them claim the down does the
// pane sub-region decide. (The corner-circle Ascend arm is gone: the
// circle lives in the bottom bar since issue #214, where a right CLICK
// ascends — bar clicks never reach the pane gesture layer.)
func Classify(in Input) Kind {
	switch {
	case in.OverEmbed:
		return EmbedHint
	case in.InGridView && in.OverTile:
		if in.InTileCenter {
			return TileCenter
		}
		return TileResize
	}
	switch {
	case in.Region.IsResize():
		// One behavior per button (issue #203): a border right-drag is a
		// SPLIT wherever it starts — over a divider exactly like at a
		// screen edge (the two cases used to diverge; now they unify).
		// Divider resizing (and pressure-closing) belongs to the left
		// button alone.
		return Split
	case in.Region.IsSwap():
		return Swap
	case in.Region.IsSplit():
		return Split
	}
	return None
}

// SplitOutcome resolves the release of a Split gesture into the final
// child-A ratio, composing the already-tested pane helpers: the gesture
// must be dragged away from its edge (SplitGestureActive), land in a
// position that leaves both children at least pane.MinPanePx
// (SplitClampedPosition — the universal minimum, issue #167), and that
// position maps to a ratio (SplitRatioFromPos). ok is false for a silent
// cancel.
func SplitOutcome(side pane.Side, paneRect pane.Rect, startX, startY, curX, curY float64) (ratio float64, ok bool) {
	if !pane.SplitGestureActive(side, startX, startY, curX, curY) {
		return 0, false
	}
	pos, ok := pane.SplitClampedPosition(side, paneRect, curX, curY)
	if !ok {
		return 0, false
	}
	return pane.SplitRatioFromPos(side, paneRect, pos), true
}

// Collapse names which side of a resize-divider gesture collapses on
// release, or neither.
type Collapse int

const (
	CollapseNone Collapse = iota
	CollapseA
	CollapseB
)

// CloseBandPx is how close to the corridor's edge the cursor must land for
// a release to close a side — "all the way across" with a small tolerance
// so a pane flush against the screen edge stays closable. Always strictly
// inside the resize range: the minimum wall sits at least pane.MinPanePx
// from the corridor edge, so a legal resize release can never read as a
// close.
const CloseBandPx = 8.0

// ResizeOutcome resolves the release of a Resize gesture: which side (if
// any) closes. corStart/corEnd are pane.CorridorSpan's bounds — the fixed
// extent of the corridor, invariant during the drag. Closing is the
// corridor-EDGE gesture (issue #204): the cursor must travel all the way
// across, to within CloseBandPx of the span's edge — reaching the start
// edge closes the A side of the grabbed split, the end edge the B side.
// The minimum walls are a resize clamp only. (Crushing past the wall used
// to close, which put the close threshold where a legal drag clamps — one
// wobble past it and a resize became an accidental close.)
func ResizeOutcome(dir pane.Direction, sx, sy, corStart, corEnd float64) Collapse {
	cursor := sx
	if dir == pane.Horizontal {
		cursor = sy
	}
	switch {
	case cursor <= corStart+CloseBandPx:
		return CollapseA
	case cursor >= corEnd-CloseBandPx:
		return CollapseB
	}
	return CollapseNone
}

// ResizeAffordance is the shared decision behind a left-button pane-boundary
// resize: whether a drag would arm a resize at the cursor, and the CSS cursor
// to advertise it. The hover-cursor path and the arm path MUST agree — the
// resize cursor has to appear exactly where a left-drag would actually resize
// — so both route through this one function instead of each re-deriving the
// gating (the two used to be hand-mirrored copies that could drift).
//
// Inputs: the region the cursor classifies into, and whether a grabbable
// divider exists on that region's side. (The corner circle no longer sits
// inside any pane's resize band — it moved to the bottom bar, issue #214 —
// so there is no precedence to arbitrate.)
func ResizeAffordance(region pane.Region, hasDivider bool) (arm bool, cursor string) {
	if !region.IsResize() || !hasDivider {
		return false, ""
	}
	switch region {
	case pane.RegionResizeLeft, pane.RegionResizeRight:
		return true, "ew-resize"
	default: // RegionResizeTop, RegionResizeBottom
		return true, "ns-resize"
	}
}
