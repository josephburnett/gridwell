package pane

// Rect is THE screen-space rectangle in logical pixels — the one shape,
// aliased by client/palette (formerly a structural duplicate).
type Rect struct {
	X, Y, W, H float64
}

// CellPx is the renderer's base cell size at zoom 1.0 — the one copy
// (formerly duplicated as a wasm const and palette.Default's literal).
const CellPx = 64.0

// Contains reports whether (x, y) lies inside the rectangle (half-open
// on the right and bottom edges, like a typical raster region).
func (r Rect) Contains(x, y float64) bool {
	return x >= r.X && y >= r.Y && x < r.X+r.W && y < r.Y+r.H
}

// Layout walks the tree and assigns each leaf pane a screen rectangle,
// recursively subdividing root by each Split's direction and ratio.
//
// The result is keyed by pane id; one entry per leaf.
func Layout(t *Tree, root Rect) map[string]Rect {
	out := map[string]Rect{}
	if t == nil {
		return out
	}
	// A zoomed pane owns the whole rect; every other pane is absent from the
	// layout (their overlays/views park via the missing-rect paths). A stale
	// Zoomed id (the pane collapsed away) falls back to the normal layout.
	if t.Zoomed != "" && t.FindPane(t.Zoomed) != nil {
		out[t.Zoomed] = root
		return out
	}
	layoutInto(t.Root, root, out)
	return out
}

func layoutInto(n TreeNode, r Rect, out map[string]Rect) {
	if n.IsLeaf() {
		out[n.Pane.ID] = r
		return
	}
	a, b := SplitRect(r, n.Split.Dir, n.Split.Ratio)
	layoutInto(n.Split.A, a, out)
	layoutInto(n.Split.B, b, out)
}

// SplitRect returns the two children rectangles for the given split. A
// horizontal split divides r into top (A) / bottom (B); a vertical split
// divides into left (A) / right (B).
func SplitRect(r Rect, dir Direction, ratio float64) (a, b Rect) {
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	if dir == Horizontal {
		hA := r.H * ratio
		a = Rect{X: r.X, Y: r.Y, W: r.W, H: hA}
		b = Rect{X: r.X, Y: r.Y + hA, W: r.W, H: r.H - hA}
		return
	}
	wA := r.W * ratio
	a = Rect{X: r.X, Y: r.Y, W: wA, H: r.H}
	b = Rect{X: r.X + wA, Y: r.Y, W: r.W - wA, H: r.H}
	return
}

// Divider describes the band along the shared edge between the two
// children of an internal Split. Used by the input layer to detect
// "right-drag the divider" gestures.
//
// Split is the underlying split (so the caller can mutate Ratio).
// ContainerRect is the parent split's full rectangle; the caller needs
// it to translate cursor positions to ratios.
type Divider struct {
	Split         *Split
	Dir           Direction
	Rect          Rect // narrow band along the divider line
	ContainerRect Rect
}

// Dividers returns one Divider per internal split in the tree, sized
// `bandPx` thick. With 0 or negative bandPx, defaults to 4 px.
func Dividers(t *Tree, root Rect, bandPx float64) []Divider {
	if bandPx <= 0 {
		bandPx = 4
	}
	var out []Divider
	if t == nil {
		return out
	}
	// A zoomed layout has no visible boundaries — offering dividers would
	// arm resizes on invisible splits (issue #80).
	if t.Zoomed != "" && t.FindPane(t.Zoomed) != nil {
		return out
	}
	collectDividers(&t.Root, root, bandPx, &out)
	return out
}

// DividerOnSide returns the index into divs of the divider directly adjacent
// to paneRect on the given side, or -1 when the pane abuts the screen edge
// with no sibling there. Adjacency: Top/Bottom needs a Horizontal divider
// whose mid-line sits at the pane's top/bottom edge; Left/Right needs a
// Vertical divider at the pane's left/right edge (within half a pixel).
// Picking the wrong divider here makes a boundary resize grab an unrelated
// split, so the match is exact and tested.
func DividerOnSide(divs []Divider, paneRect Rect, side Side) int {
	for i := range divs {
		d := divs[i]
		switch side {
		case SideTop:
			if d.Dir == Horizontal && nearHalfPx(d.Rect.Y+d.Rect.H/2, paneRect.Y) {
				return i
			}
		case SideBottom:
			if d.Dir == Horizontal && nearHalfPx(d.Rect.Y+d.Rect.H/2, paneRect.Y+paneRect.H) {
				return i
			}
		case SideLeft:
			if d.Dir == Vertical && nearHalfPx(d.Rect.X+d.Rect.W/2, paneRect.X) {
				return i
			}
		case SideRight:
			if d.Dir == Vertical && nearHalfPx(d.Rect.X+d.Rect.W/2, paneRect.X+paneRect.W) {
				return i
			}
		}
	}
	return -1
}

// nearHalfPx reports whether two pixel coordinates are within half a pixel —
// the same tolerance dragdrop.NearPx uses, inlined to keep pane dependency-free.
func nearHalfPx(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 0.5
}

// Region identifies which logical sub-area of a pane a (sx, sy) point
// falls into. Used by the right-button input layer to dispatch swap,
// split, and resize gestures purely from a hit test.
//
// The pane is conceptually divided as follows:
//
//   - A `bandPx`-thick frame near each edge: the four resize regions.
//   - The inner 1/3 × 1/3 of the *whole pane*: the swap region.
//   - The remaining annular middle, sectorized by closest edge: the
//     four split regions.
//
// At small pane sizes the inner region collapses naturally — when
// 2*bandPx ≥ W (or H), the entire pane is resize zones, which is the
// degenerate-degradation behavior we want.
type Region int

const (
	RegionNone Region = iota
	RegionSwap
	RegionResizeTop
	RegionResizeBottom
	RegionResizeLeft
	RegionResizeRight
	RegionSplitTop
	RegionSplitBottom
	RegionSplitLeft
	RegionSplitRight
)

// IsResize / IsSplit / IsSwap test the region category.
func (r Region) IsResize() bool {
	return r == RegionResizeTop || r == RegionResizeBottom ||
		r == RegionResizeLeft || r == RegionResizeRight
}
func (r Region) IsSplit() bool {
	return r == RegionSplitTop || r == RegionSplitBottom ||
		r == RegionSplitLeft || r == RegionSplitRight
}
func (r Region) IsSwap() bool { return r == RegionSwap }

// Side returns the edge side of a resize/split region. Returns
// SideTop for non-side regions (callers should test IsResize/IsSplit
// first).
func (r Region) Side() Side {
	switch r {
	case RegionResizeTop, RegionSplitTop:
		return SideTop
	case RegionResizeBottom, RegionSplitBottom:
		return SideBottom
	case RegionResizeLeft, RegionSplitLeft:
		return SideLeft
	case RegionResizeRight, RegionSplitRight:
		return SideRight
	}
	return SideTop
}

// ClassifyRegion returns the region under (sx, sy) inside pane rect r.
// Resize zones (within bandPx of any edge) take priority; then the
// center swap zone (inner 1/3 × 1/3 of the whole pane); then the
// outer split zone, sectorized by closest edge.
//
// Tiebreak when multiple edges are equidistant: top, then bottom,
// then left, then right.
func ClassifyRegion(r Rect, bandPx, sx, sy float64) Region {
	if r.W <= 0 || r.H <= 0 {
		return RegionNone
	}
	dt := sy - r.Y
	db := (r.Y + r.H) - sy
	dl := sx - r.X
	dr := (r.X + r.W) - sx
	// First-wins tiebreak across top/bottom/left/right.
	minD := dt
	side := SideTop
	if db < minD {
		minD = db
		side = SideBottom
	}
	if dl < minD {
		minD = dl
		side = SideLeft
	}
	if dr < minD {
		minD = dr
		side = SideRight
	}
	if minD < bandPx {
		switch side {
		case SideTop:
			return RegionResizeTop
		case SideBottom:
			return RegionResizeBottom
		case SideLeft:
			return RegionResizeLeft
		case SideRight:
			return RegionResizeRight
		}
	}
	// Center swap: inner 1/3 × 1/3 of the whole pane (not of the
	// post-band area). Naturally collapses to nothing as the pane
	// shrinks.
	if sx >= r.X+r.W/3 && sx < r.X+2*r.W/3 &&
		sy >= r.Y+r.H/3 && sy < r.Y+2*r.H/3 {
		return RegionSwap
	}
	switch side {
	case SideTop:
		return RegionSplitTop
	case SideBottom:
		return RegionSplitBottom
	case SideLeft:
		return RegionSplitLeft
	case SideRight:
		return RegionSplitRight
	}
	return RegionNone
}

// MinPanePx is THE minimum size of a pane side, universal across every way
// a pane can acquire a size (issue #167): the left-drag resize clamp, the
// right-drag crush-to-collapse threshold, the drag-to-split clamp, and the
// programmatic ephemeral split. One owner — a pane below this cannot be
// produced, whatever the gesture. (The resize BAND, resizeBandPx=10, is a
// different fact: how thick the grab zone is, not how small a pane may be.)
const MinPanePx = 32.0

// SplitClampedPosition projects the cursor onto the split's axis and
// clamps it to the valid range. The valid range leaves at least
// MinPanePx on each side — the universal pane minimum — so a split can
// never produce a sub-minimum pane. The second return is false when the
// cursor is outside the valid range (or the pane is too small to split at
// all), letting the caller render the preview in a "won't commit" style.
// CanSplit reports whether a pane of rect r can be split on side at all:
// both halves need MinPanePx, so the axis must exceed twice the minimum.
// The ONE sub-minimum rule — the gesture clamp (SplitClampedPosition)
// and programmatic splits (openLinkBelow) read it; they used to disagree
// at exactly 2×MinPanePx.
func CanSplit(side Side, r Rect) bool {
	switch side {
	case SideTop, SideBottom:
		return r.H > 2*MinPanePx
	default:
		return r.W > 2*MinPanePx
	}
}

func SplitClampedPosition(side Side, paneRect Rect, curX, curY float64) (float64, bool) {
	if !CanSplit(side, paneRect) {
		return 0, false
	}
	switch side {
	case SideTop, SideBottom:
		minY := paneRect.Y + MinPanePx
		maxY := paneRect.Y + paneRect.H - MinPanePx
		if curY < minY {
			return minY, false
		}
		if curY > maxY {
			return maxY, false
		}
		return curY, true
	case SideLeft, SideRight:
		minX := paneRect.X + MinPanePx
		maxX := paneRect.X + paneRect.W - MinPanePx
		if curX < minX {
			return minX, false
		}
		if curX > maxX {
			return maxX, false
		}
		return curX, true
	}
	return 0, false
}

// SplitRatioFromPos is the inverse of a clamped split position: given a
// committed split on `side` of `paneRect`, it returns the ratio (in pane-
// fraction terms) the NEW pane on that side should occupy. Top/Left put the
// new pane in the A child measured from the near edge; Bottom/Right measure
// from the far edge. Pairs with SplitClampedPosition (which produces `pos`).
func SplitRatioFromPos(side Side, paneRect Rect, pos float64) float64 {
	switch side {
	case SideTop:
		return (pos - paneRect.Y) / paneRect.H
	case SideBottom:
		return ((paneRect.Y + paneRect.H) - pos) / paneRect.H
	case SideLeft:
		return (pos - paneRect.X) / paneRect.W
	case SideRight:
		return ((paneRect.X + paneRect.W) - pos) / paneRect.W
	}
	return 0.5
}

func collectDividers(n *TreeNode, r Rect, bandPx float64, out *[]Divider) {
	if n.IsLeaf() {
		return
	}
	a, b := SplitRect(r, n.Split.Dir, n.Split.Ratio)
	var div Rect
	if n.Split.Dir == Horizontal {
		// Horizontal divider line at y = a.Y + a.H, full width.
		div = Rect{X: r.X, Y: a.Y + a.H - bandPx/2, W: r.W, H: bandPx}
	} else {
		// Vertical divider at x = a.X + a.W, full height.
		div = Rect{X: a.X + a.W - bandPx/2, Y: r.Y, W: bandPx, H: r.H}
	}
	*out = append(*out, Divider{
		Split:         n.Split,
		Dir:           n.Split.Dir,
		Rect:          div,
		ContainerRect: r,
	})
	collectDividers(&n.Split.A, a, bandPx, out)
	collectDividers(&n.Split.B, b, bandPx, out)
}
