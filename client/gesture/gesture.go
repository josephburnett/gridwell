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
	Split
	// Resize drags a pane divider, collapsing a side that shrinks past the
	// close threshold.
	Resize
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

	// OnCornerCircle is true when the cursor is in the pane's corner
	// circle; CanAscend is true when the pane has somewhere to ascend to.
	// Together they arm Ascend (at root CanAscend is false and the circle
	// is just the creation +, handled elsewhere).
	OnCornerCircle bool
	CanAscend      bool

	// InGridView is true when the pane shows a grid (p.TextFocus == 0) —
	// tile gestures are only valid there. OverTile is true when the cursor
	// is over a tile; InTileCenter is true when it's in that tile's inner
	// third (the clone handle) rather than the resize ring.
	InGridView   bool
	OverTile     bool
	InTileCenter bool

	// Region is the pane sub-region under the cursor (resize band / split
	// edge / swap center); HasDividerOnSide is true when an actual pane
	// divider abuts Region.Side(). A resize band with no divider falls
	// through to a split so the gesture stays useful at a screen edge.
	Region           pane.Region
	HasDividerOnSide bool
}

// Classify maps the resolved facts to a gesture Kind. The two switches
// mirror onRightDown exactly: the special targets (embed, corner circle,
// URL center, tile) take priority in that order; only if none of them
// claim the down does the pane sub-region decide.
func Classify(in Input) Kind {
	switch {
	case in.OverEmbed:
		return EmbedHint
	case in.OnCornerCircle && in.CanAscend:
		return Ascend
	case in.InGridView && in.OverTile:
		if in.InTileCenter {
			return TileCenter
		}
		return TileResize
	}
	switch {
	case in.Region.IsResize():
		// A resize band over an actual divider grabs it; a resize band at
		// a screen edge (no divider) falls through to a split.
		if in.HasDividerOnSide {
			return Resize
		}
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
// position that leaves both children at least bandPx (SplitClampedPosition),
// and that position maps to a ratio (SplitRatioFromPos). ok is false for a
// silent cancel.
func SplitOutcome(side pane.Side, paneRect pane.Rect, bandPx, startX, startY, curX, curY float64) (ratio float64, ok bool) {
	if !pane.SplitGestureActive(side, startX, startY, curX, curY) {
		return 0, false
	}
	pos, ok := pane.SplitClampedPosition(side, paneRect, bandPx, curX, curY)
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

// ResizeOutcome resolves the release of a Resize gesture into the final
// divider ratio plus which child (if any) shrank past closeThreshold and
// should collapse. The ratio comes from the cursor; the collapse decision
// compares each child's resulting size along the split axis.
func ResizeOutcome(container pane.Rect, dir pane.Direction, sx, sy, closeThreshold float64) (ratio float64, collapse Collapse) {
	ratio = pane.RatioFromCursor(container, dir, sx, sy)
	var aSize, bSize float64
	if dir == pane.Horizontal {
		aSize = container.H * ratio
		bSize = container.H * (1 - ratio)
	} else {
		aSize = container.W * ratio
		bSize = container.W * (1 - ratio)
	}
	switch {
	case aSize < closeThreshold:
		return ratio, CollapseA
	case bSize < closeThreshold:
		return ratio, CollapseB
	}
	return ratio, CollapseNone
}
