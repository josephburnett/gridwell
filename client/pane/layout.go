package pane

// Rect is a screen-space rectangle in pixels.
type Rect struct {
	X, Y, W, H float64
}

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
	collectDividers(&t.Root, root, bandPx, &out)
	return out
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

// RatioFromCursor projects the cursor (sx, sy) onto the divider's
// axis inside the parent split's container rect, and returns the
// resulting split ratio in [0, 1]. Useful for live divider drags:
// the caller can call SetRatio (or directly mutate Split.Ratio) with
// this value on every mousemove tick.
//
// dir picks the axis: Horizontal divider → projects onto y;
// Vertical divider → onto x. Cursor positions outside the container
// are clamped to the edges.
func RatioFromCursor(container Rect, dir Direction, sx, sy float64) float64 {
	var num, denom float64
	if dir == Horizontal {
		num = sy - container.Y
		denom = container.H
	} else {
		num = sx - container.X
		denom = container.W
	}
	if denom <= 0 {
		return 0.5
	}
	r := num / denom
	if r < 0 {
		r = 0
	}
	if r > 1 {
		r = 1
	}
	return r
}

// SplitGestureActive reports whether the cursor at (curX, curY) has
// moved past the start (startX, startY) in the direction expected for
// the given side: top expects a downward drag, bottom an upward drag,
// left a rightward drag, and right a leftward drag. The "active" state
// drives the split preview line color (blue vs grey).
func SplitGestureActive(side Side, startX, startY, curX, curY float64) bool {
	switch side {
	case SideTop:
		return curY > startY
	case SideBottom:
		return curY < startY
	case SideLeft:
		return curX > startX
	case SideRight:
		return curX < startX
	}
	return false
}

// SplitClampedPosition projects the cursor onto the split's axis and
// clamps it to the valid range. The valid range leaves at least
// 2*bandPx on each side so both resulting panes can hold a full resize
// band. The second return is false when the cursor is outside the
// valid range (or the pane is too small to split at all), letting the
// caller render the preview in a "won't commit" style.
func SplitClampedPosition(side Side, paneRect Rect, bandPx, curX, curY float64) (float64, bool) {
	switch side {
	case SideTop, SideBottom:
		minY := paneRect.Y + 2*bandPx
		maxY := paneRect.Y + paneRect.H - 2*bandPx
		if minY >= maxY {
			return 0, false
		}
		if curY < minY {
			return minY, false
		}
		if curY > maxY {
			return maxY, false
		}
		return curY, true
	case SideLeft, SideRight:
		minX := paneRect.X + 2*bandPx
		maxX := paneRect.X + paneRect.W - 2*bandPx
		if minX >= maxX {
			return 0, false
		}
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

// ClampRatioToMinPx keeps a split ratio within [minR, 1-minR], where minR is
// minPx expressed as a fraction of the container's extent along the split
// axis (its height for a Horizontal split, width for Vertical). This reserves
// at least minPx for each side so neither child collapses. A degenerate
// container (extent <= 0) returns the ratio unchanged; minR is itself capped
// at 0.5 so the clamp range never inverts.
func ClampRatioToMinPx(container Rect, dir Direction, ratio, minPx float64) float64 {
	extent := container.W
	if dir == Horizontal {
		extent = container.H
	}
	if extent <= 0 {
		return ratio
	}
	minR := minPx / extent
	if minR > 0.5 {
		minR = 0.5
	}
	if ratio < minR {
		return minR
	}
	if ratio > 1-minR {
		return 1 - minR
	}
	return ratio
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
