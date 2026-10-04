package pane

import "github.com/josephburnett/gridwell/client/zoomtrans"

// Rect is the screen-space rectangle in logical pixels, one shape, aliased by
// client/palette.
type Rect struct {
	X, Y, W, H float64
}

// CellPx is the renderer's base cell size at zoom 1, the one copy.
const CellPx = 64.0

// Size is r's extent as the one input framing math measures against, false
// for a rect with no area: a pane absent from the layout has no framing.
func (r Rect) Size() (zoomtrans.Size, bool) { return zoomtrans.SizeOf(r.W, r.H) }

// Contains is half-open on the right and bottom edges.
func (r Rect) Contains(x, y float64) bool {
	return x >= r.X && y >= r.Y && x < r.X+r.W && y < r.Y+r.H
}

// Layout assigns each leaf pane a screen rectangle, keyed by pane id.
func Layout(t *Tree, root Rect) map[string]Rect {
	out := map[string]Rect{}
	if t == nil {
		return out
	}
	// A zoomed pane owns the whole rect and every other pane is absent, so
	// their surfaces park through the missing-rect paths. A stale Zoomed id
	// falls back to the normal layout.
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

// SplitRect is the two child rectangles: A is the top or left one.
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

// Divider is the band along the shared edge of a Split's two children. Split
// is the underlying split, so the caller can mutate Ratio, and ContainerRect
// is what turns a cursor position into one.
type Divider struct {
	Split         *Split
	Dir           Direction
	Rect          Rect // narrow band along the divider line
	ContainerRect Rect
}

// Dividers is one Divider per internal split, bandPx thick, defaulting to 4.
func Dividers(t *Tree, root Rect, bandPx float64) []Divider {
	if bandPx <= 0 {
		bandPx = 4
	}
	var out []Divider
	if t == nil {
		return out
	}
	// A zoomed layout has no visible boundaries, so offering dividers would
	// arm resizes on invisible splits.
	if t.Zoomed != "" && t.FindPane(t.Zoomed) != nil {
		return out
	}
	collectDividers(&t.Root, root, bandPx, &out)
	return out
}

// DividerOnSide indexes the divider adjacent to paneRect on side, or -1 when
// the pane abuts the screen edge. Adjacency is a divider of the matching
// orientation whose mid-line sits on that edge within half a pixel; a looser
// match would let a boundary resize grab an unrelated split.
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

// DividerGrab is which dividers a press grabs, the one decision behind arming
// a boundary resize. At a corner the press is inside both a horizontal and a
// vertical edge band, so it grabs both and the drag moves both axes. There is
// at most one per axis, since a pane has one edge per side. The zero value
// grabs nothing and GrabDividers is the only producer.
type DividerGrab struct {
	// The horizontal divider along the pane's top or bottom edge. Horiz
	// indexes the divs slice the grab was resolved against.
	HasHoriz  bool
	Horiz     int
	HorizSide Side
	// The vertical divider along the left or right edge.
	HasVert  bool
	Vert     int
	VertSide Side
}

func (g DividerGrab) Any() bool { return g.HasHoriz || g.HasVert }

// Both is a corner grab: one divider per axis, driven by one gesture.
func (g DividerGrab) Both() bool { return g.HasHoriz && g.HasVert }

// GrabDividers decides which dividers a press at (sx, sy) in pane rect r
// grabs. Each axis is decided on its own: the nearer of the pane's two edges
// wins, ties going to top and left as in ClassifyRegion, it must lie within
// bandPx, and a divider must be adjacent there. Two axes is the corner, and
// the caller arms one ordinary resize per axis rather than a gesture of its
// own.
func GrabDividers(divs []Divider, r Rect, bandPx, sx, sy float64) DividerGrab {
	var g DividerGrab
	if r.W <= 0 || r.H <= 0 {
		return g
	}
	if side, in := nearerSide(sy-r.Y, (r.Y+r.H)-sy, SideTop, SideBottom, bandPx); in {
		if i := DividerOnSide(divs, r, side); i >= 0 {
			g.HasHoriz, g.Horiz, g.HorizSide = true, i, side
		}
	}
	if side, in := nearerSide(sx-r.X, (r.X+r.W)-sx, SideLeft, SideRight, bandPx); in {
		if i := DividerOnSide(divs, r, side); i >= 0 {
			g.HasVert, g.Vert, g.VertSide = true, i, side
		}
	}
	return g
}

// nearerSide picks the closer of a pane's two edges along one axis, the near
// one winning a tie, and reports whether it is inside the grab band.
func nearerSide(dNear, dFar float64, near, far Side, bandPx float64) (Side, bool) {
	if dFar < dNear {
		return far, dFar < bandPx
	}
	return near, dNear < bandPx
}

func nearHalfPx(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 0.5
}

// Region is which sub-area of a pane a point falls into, so the right-button
// layer dispatches swap, split and resize from one hit test: a bandPx frame at
// each edge resizes, the inner third of the whole pane swaps, and the annulus
// between them splits by closest edge. The inner regions collapse naturally as
// the pane shrinks.
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

func (r Region) IsResize() bool {
	return r == RegionResizeTop || r == RegionResizeBottom ||
		r == RegionResizeLeft || r == RegionResizeRight
}
func (r Region) IsSplit() bool {
	return r == RegionSplitTop || r == RegionSplitBottom ||
		r == RegionSplitLeft || r == RegionSplitRight
}
func (r Region) IsSwap() bool { return r == RegionSwap }

// Side is the edge a resize or split region names, SideTop for anything else,
// so callers test IsResize or IsSplit first.
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

// ClassifyRegion is the region under (sx, sy) in pane rect r, resize first,
// then swap, then split. Equidistant edges break top, bottom, left, right.
func ClassifyRegion(r Rect, bandPx, sx, sy float64) Region {
	if r.W <= 0 || r.H <= 0 {
		return RegionNone
	}
	dt := sy - r.Y
	db := (r.Y + r.H) - sy
	dl := sx - r.X
	dr := (r.X + r.W) - sx
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
	// The inner third of the whole pane, not of the post-band area.
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

// MinPanePx is the minimum size of a pane side, across every way a pane can
// acquire one, so no gesture can produce a pane below it. The resize band,
// wasm's resizeBandPx, is a different fact: how thick the grab zone is.
const MinPanePx = 32.0

// CanSplit reports whether a pane of rect r can be split on side at all: both
// halves need MinPanePx, so the axis must exceed twice it. It is the one
// sub-minimum rule, read by SplitClampedPosition and by programmatic splits
// alike.
func CanSplit(side Side, r Rect) bool {
	switch side {
	case SideTop, SideBottom:
		return r.H > 2*MinPanePx
	default:
		return r.W > 2*MinPanePx
	}
}

// SplitClampedPosition projects the cursor onto the split's axis and clamps it
// to leave MinPanePx on each side. The second return is false when the cursor
// was outside that range, or the pane is too small to split, so the caller can
// render the preview as one that will not commit.
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

// SplitRatioFromPos is SplitClampedPosition's inverse: the pane fraction the
// new pane on side occupies. Top and left measure from the near edge, bottom
// and right from the far one.
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
		div = Rect{X: r.X, Y: a.Y + a.H - bandPx/2, W: r.W, H: bandPx}
	} else {
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
