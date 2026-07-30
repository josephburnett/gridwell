package pane

// resize.go — cascading ("resize-through") divider drags, issue #79. Dragging
// a divider moves that BOUNDARY LINE tmux-style: the pane adjacent to it
// compresses to its minimum first, then the next pane along the axis, and so
// on — including across same-axis ancestor splits — until the sum of minimums
// walls the drag. The old behavior wrote one Split.Ratio and squashed the
// whole opposite subtree proportionally, stopping at minPx for the side as a
// whole.
//
// The implementation flattens the CORRIDOR — the maximal run of same-axis
// subtrees around the dragged boundary — into an ordered size list, moves the
// boundary with sequential compression, and writes the sizes back as ratios.
// Perpendicular subtrees ride along whole: their cross-size changes, their
// internal ratios never do. Pure; unit-tested without a canvas.

// CorridorWalls returns the [lo, hi] cursor bounds of target's boundary
// drag: the positions where everything between the boundary and the
// corridor's start (lo) or end (hi) sits at its minimum. This is THE wall —
// the same bounds ResizeThrough clamps the live drag to, and the bounds the
// release compares the cursor against to decide a collapse (crushing PAST
// the wall is the close gesture). One owner: the release verdict reading a
// different geometry (the grabbed split's own container) is exactly the bug
// that closed panes on a legal mid-corridor release.
func CorridorWalls(root TreeNode, rootRect Rect, target *Split, minPx float64) (lo, hi float64, ok bool) {
	top, topRect, ok := corridorTop(root, rootRect, target)
	if !ok {
		return 0, 0, false
	}
	topNode := TreeNode{Split: top}
	segs := flattenCorridor(topNode, target.Dir)
	start, total := axisSpan(topRect, target.Dir)
	bIdx := boundaryIndex(segs, target)
	if bIdx < 0 || bIdx+1 >= len(segs) {
		return 0, 0, false
	}
	lo = start
	for _, s := range segs[:bIdx+1] {
		lo += minSize(s, target.Dir, minPx)
	}
	hi = start + total
	for _, s := range segs[bIdx+1:] {
		hi -= minSize(s, target.Dir, minPx)
	}
	return lo, hi, true
}

// crushEps absorbs float ratio noise in the pressed-past-the-bump compare:
// a segment reads as crushed only when the cursor is strictly past the
// position where it reached its minimum, so a bare click (cursor at the
// bump of an already-minimal neighbor) never closes anything.
const crushEps = 0.5

// CrushPlan is the progressive close model (issue #217, superseding #204's
// corridor-edge band), captured at ARM time: for each corridor segment
// outward from the grabbed boundary, the bump threshold — the cursor
// position at which that segment has been pressed to its minimum. Bumps are
// defined against the sizes AT GRAB (pressing past where a segment's border
// was reachable), so a drag that merely returns to a pre-crushed segment's
// border reds nothing until it presses beyond it.
type CrushPlan struct {
	// ASegs/AThresh: segments before the boundary, adjacent-first, with the
	// cursor position that presses each to its minimum (strictly toward the
	// corridor start). BSegs/BThresh mirror after the boundary.
	ASegs, BSegs     []TreeNode
	AThresh, BThresh []float64
}

// PlanCrush captures the crush thresholds for target's boundary at arm.
func PlanCrush(root TreeNode, rootRect Rect, target *Split, minPx float64) (CrushPlan, bool) {
	top, topRect, ok := corridorTop(root, rootRect, target)
	if !ok {
		return CrushPlan{}, false
	}
	topNode := TreeNode{Split: top}
	all := flattenCorridor(topNode, target.Dir)
	start, total := axisSpan(topRect, target.Dir)
	sizes := segmentSizes(topNode, target.Dir, total)
	bIdx := boundaryIndex(all, target)
	if bIdx < 0 || bIdx+1 >= len(all) {
		return CrushPlan{}, false
	}
	boundary := start
	for _, sz := range sizes[:bIdx+1] {
		boundary += sz
	}
	var plan CrushPlan
	th := boundary
	for i := bIdx; i >= 0; i-- {
		th -= sizes[i] - minSize(all[i], target.Dir, minPx)
		plan.ASegs = append(plan.ASegs, all[i])
		plan.AThresh = append(plan.AThresh, th)
	}
	th = boundary
	for i := bIdx + 1; i < len(all); i++ {
		th += sizes[i] - minSize(all[i], target.Dir, minPx)
		plan.BSegs = append(plan.BSegs, all[i])
		plan.BThresh = append(plan.BThresh, th)
	}
	return plan, true
}

// Red returns the segments the cursor has pressed to close — the contiguous
// boundary-adjacent run whose bumps the cursor is strictly past, on the side
// it is pressing. Backing off shrinks the run in reverse; the preview and
// the release both read this one verdict, so they cannot disagree.
func (cp CrushPlan) Red(cursorPx float64) []TreeNode {
	var out []TreeNode
	for k, t := range cp.AThresh {
		if cursorPx < t-crushEps {
			out = append(out, cp.ASegs[k])
		} else {
			break
		}
	}
	if len(out) > 0 {
		return out
	}
	for k, t := range cp.BThresh {
		if cursorPx > t+crushEps {
			out = append(out, cp.BSegs[k])
		} else {
			break
		}
	}
	return out
}

// SegmentRects returns the LIVE rects of the given corridor segments (for
// the red close overlay) — recomputed from current sizes so the overlay
// tracks the crush, never a stale arm-time copy.
func SegmentRects(root TreeNode, rootRect Rect, target *Split, want []TreeNode) []Rect {
	top, topRect, ok := corridorTop(root, rootRect, target)
	if !ok {
		return nil
	}
	topNode := TreeNode{Split: top}
	all := flattenCorridor(topNode, target.Dir)
	_, total := axisSpan(topRect, target.Dir)
	sizes := segmentSizes(topNode, target.Dir, total)
	start, _ := axisSpan(topRect, target.Dir)
	out := make([]Rect, 0, len(want))
	for _, w := range want {
		off := start
		for i, s := range all {
			if s == w {
				r := topRect
				if target.Dir == Horizontal {
					r.Y, r.H = off, sizes[i]
				} else {
					r.X, r.W = off, sizes[i]
				}
				out = append(out, r)
				break
			}
			off += sizes[i]
		}
	}
	return out
}

// RemoveSegment removes a corridor segment (an arbitrary subtree) from the
// tree, hoisting its sibling — the close half of the crush gesture. The
// caller flushes the subtree's leaves first. Removing the whole tree is
// refused; focus moves to a surviving leaf when it was inside the removed
// subtree.
func (t *Tree) RemoveSegment(seg TreeNode) bool {
	if t.Root == seg {
		return false
	}
	var rec func(n *TreeNode) bool
	rec = func(n *TreeNode) bool {
		if n.Split == nil {
			return false
		}
		if n.Split.A == seg {
			*n = n.Split.B
			return true
		}
		if n.Split.B == seg {
			*n = n.Split.A
			return true
		}
		return rec(&n.Split.A) || rec(&n.Split.B)
	}
	if !rec(&t.Root) {
		return false
	}
	t.Zoomed = "" // structural edit, same rule as CollapseSplit
	if t.FindPane(t.Focus) == nil {
		t.Focus = anyLeafID(t.Root)
	}
	return true
}

// LocateSplit returns target's CURRENT laid-out container rect within the
// tree. The live geometry — a drag preview reading a rect captured at arm
// time goes stale the moment a cascade moves an ancestor ratio.
func LocateSplit(root TreeNode, rootRect Rect, target *Split) (Rect, bool) {
	var locate func(n TreeNode, r Rect) (Rect, bool)
	locate = func(n TreeNode, r Rect) (Rect, bool) {
		if n.Split == nil {
			return Rect{}, false
		}
		if n.Split == target {
			return r, true
		}
		a, b := SplitRect(r, n.Split.Dir, n.Split.Ratio)
		if rr, ok := locate(n.Split.A, a); ok {
			return rr, true
		}
		return locate(n.Split.B, b)
	}
	return locate(root, rootRect)
}

// ResizeThrough moves target's divider toward cursorPx (a coordinate along
// target.Dir's axis), cascading compression through the corridor with each
// leaf pane clamped to minPx. root/rootRect are the whole tree, so the
// corridor can cross target's same-axis ancestors.
func ResizeThrough(root TreeNode, rootRect Rect, target *Split, cursorPx, minPx float64) {
	lo, hi, ok := CorridorWalls(root, rootRect, target, minPx)
	if !ok {
		return
	}
	top, topRect, ok := corridorTop(root, rootRect, target)
	if !ok {
		return
	}
	topNode := TreeNode{Split: top}
	segs := flattenCorridor(topNode, target.Dir)
	start, total := axisSpan(topRect, target.Dir)
	sizes := segmentSizes(topNode, target.Dir, total)
	bIdx := boundaryIndex(segs, target)
	if bIdx < 0 || bIdx+1 >= len(segs) {
		return
	}
	pos := cursorPx
	if pos < lo {
		pos = lo
	}
	if pos > hi {
		pos = hi
	}

	cur := start
	for _, sz := range sizes[:bIdx+1] {
		cur += sz
	}
	delta := pos - cur
	if delta == 0 {
		return
	}

	// Shrink one side sequentially from the boundary outward; the growth all
	// goes to the segment adjacent to the boundary on the other side.
	if delta < 0 {
		takeSequential(segs[:bIdx+1], sizes[:bIdx+1], -delta, target.Dir, minPx, true)
		sizes[bIdx+1] += -delta
	} else {
		takeSequential(segs[bIdx+1:], sizes[bIdx+1:], delta, target.Dir, minPx, false)
		sizes[bIdx] += delta
	}

	applySizes(topNode, target.Dir, segs, sizes)
}

// corridorTop returns target's topmost SAME-AXIS ancestor split (possibly
// target itself) and its laid-out rect: the corridor the boundary drag can
// reach. A perpendicular parent (or the root) ends the climb.
func corridorTop(root TreeNode, rootRect Rect, target *Split) (*Split, Rect, bool) {
	// Path from root to target, parents first.
	var path []*Split
	var find func(n TreeNode) bool
	find = func(n TreeNode) bool {
		if n.Split == nil {
			return false
		}
		if n.Split == target {
			return true
		}
		if find(n.Split.A) || find(n.Split.B) {
			path = append(path, n.Split)
			return true
		}
		return false
	}
	if !find(root) {
		if root.Split != target {
			return nil, Rect{}, false
		}
	}
	// Climb while the immediate parent splits along the same axis.
	top := target
	for _, parent := range path { // path is child-most last? appended on unwind → parents in leaf→root order
		if parent.Dir == target.Dir && (parent.A.Split == top || parent.B.Split == top) {
			top = parent
			continue
		}
		break
	}
	// Lay out from the root to find top's rect.
	var locate func(n TreeNode, r Rect) (Rect, bool)
	locate = func(n TreeNode, r Rect) (Rect, bool) {
		if n.Split == nil {
			return Rect{}, false
		}
		if n.Split == top {
			return r, true
		}
		a, b := SplitRect(r, n.Split.Dir, n.Split.Ratio)
		if rr, ok := locate(n.Split.A, a); ok {
			return rr, true
		}
		return locate(n.Split.B, b)
	}
	r, ok := locate(root, rootRect)
	return top, r, ok
}

// flattenCorridor lists, in axis order, the maximal subtrees of n that are
// NOT same-axis splits — the corridor segments.
func flattenCorridor(n TreeNode, dir Direction) []TreeNode {
	if n.Split == nil || n.Split.Dir != dir {
		return []TreeNode{n}
	}
	return append(flattenCorridor(n.Split.A, dir), flattenCorridor(n.Split.B, dir)...)
}

// segmentSizes computes each corridor segment's current size along the axis
// by walking the same ratio tree flattenCorridor flattened.
func segmentSizes(n TreeNode, dir Direction, total float64) []float64 {
	if n.Split == nil || n.Split.Dir != dir {
		return []float64{total}
	}
	a := segmentSizes(n.Split.A, dir, total*n.Split.Ratio)
	return append(a, segmentSizes(n.Split.B, dir, total*(1-n.Split.Ratio))...)
}

// boundaryIndex finds the segment index the dragged boundary sits AFTER: the
// last segment of target.A's flattened run.
func boundaryIndex(segs []TreeNode, target *Split) int {
	aSegs := flattenCorridor(target.A, target.Dir)
	last := aSegs[len(aSegs)-1]
	for i, s := range segs {
		if s == last {
			return i
		}
	}
	return -1
}

// takeSequential removes `need` from the segment sizes, starting adjacent to
// the boundary and walking outward (fromEnd walks the slice backward),
// clamping each segment at its min. The caller guaranteed capacity via the
// lo/hi walls.
func takeSequential(segs []TreeNode, sizes []float64, need float64, dir Direction, minPx float64, fromEnd bool) {
	for k := range segs {
		if need <= 0 {
			return
		}
		i := k
		if fromEnd {
			i = len(segs) - 1 - k
		}
		avail := sizes[i] - minSize(segs[i], dir, minPx)
		if avail <= 0 {
			continue
		}
		take := need
		if take > avail {
			take = avail
		}
		sizes[i] -= take
		need -= take
	}
}

// applySizes writes the segment sizes back as ratios over the same-axis tree.
func applySizes(n TreeNode, dir Direction, segs []TreeNode, sizes []float64) {
	var apply func(m TreeNode) float64 // returns subtree's new total extent
	apply = func(m TreeNode) float64 {
		if m.Split == nil || m.Split.Dir != dir {
			for i, s := range segs {
				if s == m {
					return sizes[i]
				}
			}
			return 0
		}
		a := apply(m.Split.A)
		b := apply(m.Split.B)
		if a+b > 0 {
			m.Split.Ratio = a / (a + b)
		}
		return a + b
	}
	apply(n)
}

// minSize is the smallest extent a subtree can be squeezed to along dir:
// minPx for a leaf, the SUM of both children for a same-axis split, the MAX
// for a perpendicular one (its children share the extent).
func minSize(n TreeNode, dir Direction, minPx float64) float64 {
	if n.Split == nil {
		return minPx
	}
	a := minSize(n.Split.A, dir, minPx)
	b := minSize(n.Split.B, dir, minPx)
	if n.Split.Dir == dir {
		return a + b
	}
	if a > b {
		return a
	}
	return b
}

// axisSpan returns (start, extent) of r along dir's axis.
func axisSpan(r Rect, dir Direction) (float64, float64) {
	if dir == Horizontal {
		return r.Y, r.H
	}
	return r.X, r.W
}
