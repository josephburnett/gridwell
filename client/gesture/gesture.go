// Package gesture holds the pure decision logic for the right-button
// gesture surface: classifying a right-button-down into a gesture Kind,
// and resolving the pure-math gestures on release into a concrete result.
//
// It owns only the parts that don't touch the DOM, the pane tree, or the
// store. The App computes the resolved facts (is the cursor over a tile?
// is there a divider on this side? is this a URL descent?) from its own
// lookups and hands them in as an Input; gesture.Classify encodes the
// priority ordering between those facts — the piece worth testing in
// isolation. On release, the gestures whose outcome is pure geometry (Split,
// Resize, URLRefresh, Ascend) resolve here; the gestures whose outcome is a
// drop resolution (a TileCenter copy or link, drop-on-+-button delete) stay
// in the App.
package gesture

import "github.com/josephburnett/gridwell/client/pane"

// Kind is the classification of an in-flight right-button gesture, fixed
// at right-button-down and never changed mid-gesture. None means the
// right-down armed nothing (e.g. an empty region with no divider).
type Kind int

const (
	None Kind = iota
	// Ascend is armed on the corner +/refresh/back circle when the pane
	// has somewhere to ascend to. Release inside the circle ascends;
	// dragging out cancels.
	Ascend
	// TileCenter is the copy/link grab handle: armed in a tile's inner
	// third. Drag past the threshold clones the tile, or links it when ctrl
	// was held at the press; bare release is a no-op.
	TileCenter
	// TileResize is armed on a tile outside its center: rubber-band the
	// footprint from the diagonally-opposite pinned corner.
	TileResize
	// Swap exchanges the origin pane with the pane under the cursor at
	// release.
	Swap
	// Split splits the pane along the armed side at the release ratio. The
	// right button always splits from a border, over a divider or at a
	// screen edge alike; resizing, and closing under pressure, is the left
	// button's job.
	Split
)

// Input is the set of resolved facts at right-button-down. Every field is
// computed by the App from its own lookups; Classify only orders them.
// The booleans are evaluated in priority order, so a field only matters
// when every higher-priority field is false.
type Input struct {
	// InGridView is true when the pane shows a grid (p.TextFocus == 0) —
	// tile gestures are only valid there. OverTile is true when the cursor
	// is over a tile; InTileCenter is true when it's in that tile's inner
	// third (the copy/link handle) rather than the resize ring.
	InGridView   bool
	OverTile     bool
	InTileCenter bool

	// Region is the pane sub-region under the cursor (resize band / split
	// edge / swap center).
	Region pane.Region
}

// Classify maps the resolved facts to a gesture Kind. The two switches
// mirror onRightDown exactly: the special target (a tile under the cursor)
// takes priority, and only if it doesn't claim the down does the pane
// sub-region decide. The circle lives in the bottom bar and the bar's ascent
// is a crumb left-click, so no bar gesture reaches this layer.
func Classify(in Input) Kind {
	switch {
	case in.InGridView && in.OverTile:
		if in.InTileCenter {
			return TileCenter
		}
		return TileResize
	}
	switch {
	case in.Region.IsResize():
		// One behavior per button: a border right-drag is a split wherever
		// it starts, over a divider exactly like at a screen edge. Divider
		// resizing, and pressure-closing, belongs to the left button.
		return Split
	case in.Region.IsSwap():
		return Swap
	case in.Region.IsSplit():
		return Split
	}
	return None
}

// SplitOutcome resolves the release of a Split gesture into the final
// ratio for the HOST pane (the pane the cursor is in at release), with the
// side already resolved by SplitSideFromDrag: the cursor must land in a
// position that leaves both children at least pane.MinPanePx
// (SplitClampedPosition), and that position maps to a ratio
// (SplitRatioFromPos). ok is false for a silent cancel.
func SplitOutcome(side pane.Side, paneRect pane.Rect, curX, curY float64) (ratio float64, ok bool) {
	pos, ok := pane.SplitClampedPosition(side, paneRect, curX, curY)
	if !ok {
		return 0, false
	}
	return pane.SplitRatioFromPos(side, paneRect, pos), true
}

// SplitSideFromDrag resolves a right-drag split's side from the drag, not the
// grab: dragging toward the axis-positive direction opens the new pane on the
// leading side of whatever pane the cursor is in — the space between the
// grabbed border and the cursor — so either side of a border behaves
// identically and the direction can flip mid-gesture.
// active is false until the drag clears SplitArmPx — a bare click or jitter
// commits nothing.
func SplitSideFromDrag(axis pane.Direction, startX, startY, curX, curY float64) (side pane.Side, active bool) {
	d := curX - startX
	if axis == pane.Horizontal {
		d = curY - startY
	}
	if d > SplitArmPx {
		if axis == pane.Horizontal {
			return pane.SideTop, true
		}
		return pane.SideLeft, true
	}
	if d < -SplitArmPx {
		if axis == pane.Horizontal {
			return pane.SideBottom, true
		}
		return pane.SideRight, true
	}
	return 0, false
}

// SplitArmPx is the drag distance that arms a split: below it a release is
// a silent cancel, so a bare right-click on a border never splits.
const SplitArmPx = 8.0

// ResizeAffordance is the shared decision behind a left-button pane-boundary
// resize: whether a drag would arm a resize at the cursor, and the CSS cursor
// to advertise it. The hover-cursor path and the arm path must agree — the
// resize cursor has to appear exactly where a left-drag would resize — so
// both route through this one function instead of each re-deriving the
// gating.
//
// Input: the grab — which dividers the press is close enough to take, at most
// one per axis (pane.GrabDividers). A grab on both axes is a corner, and the
// cursor names the diagonal it opens along, so the corner is as discoverable
// as a single divider.
func ResizeAffordance(g pane.DividerGrab) (arm bool, cursor string) {
	if !g.Any() {
		return false, ""
	}
	switch {
	case g.Both():
		// Top-left and bottom-right corners run NW–SE; the other two NE–SW.
		if (g.HorizSide == pane.SideTop) == (g.VertSide == pane.SideLeft) {
			return true, "nwse-resize"
		}
		return true, "nesw-resize"
	case g.HasVert:
		return true, "ew-resize"
	default: // horizontal only
		return true, "ns-resize"
	}
}
