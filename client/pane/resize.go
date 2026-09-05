package pane

// Cascading ("resize-through") divider drags. Dragging a divider moves that
// boundary line tmux-style: the pane adjacent to it compresses to its minimum
// first, then the next pane along the axis, and so on — across same-axis
// ancestor splits included — until the sum of minimums walls the drag.
//
// The implementation flattens the corridor — the maximal run of same-axis
// subtrees around the dragged boundary — into an ordered size list, moves the
// boundary with sequential compression, and writes the sizes back as ratios.
// Perpendicular subtrees ride along whole: their cross-size changes, their
// internal ratios never do. Pure, and unit-tested without a canvas.

// CorridorWalls returns the [lo, hi] cursor bounds of target's boundary
// drag: the positions where everything between the boundary and the
// corridor's start (lo) or end (hi) sits at its minimum. This is the wall:
// the same bounds ResizeThrough clamps the live drag to, and the bounds the
// release compares the cursor against to decide a collapse, since crushing
// past the wall is the close gesture. One owner — a release verdict that read
// a different geometry, such as the grabbed split's own container, would
// close panes on a legal mid-corridor release.
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

// crushEps absorbs float ratio noise in the pressed-past-the-bump compare
// and widens each live threshold into a small sticky band (CrushPlan.Update):
// a segment reds only when the cursor is strictly past the position where
// it sits at its minimum, so a bare click (cursor at the bump of an
// already-minimal neighbor) never closes anything.
const crushEps = 0.5

// CrushPlan is the progressive close model: for each corridor segment outward
// from the grabbed boundary, red state driven by a per-move threshold — the
// position where that segment sits pressed to its minimum in the current
// layout (its live edge plus its min). On the way in, segments beyond the
// pressed one still hold their slack, so the bump is where the pane visibly
// bottoms out: pressure builds as soon as you hit another border, and closing
// a middle pane needs no travel to the screen edge. On the way out after a
// deep multi-segment crush those outer segments sit at min, so the red clears
// just past the wall — backing off about one minimum width leaves every
// crushed pane at min and closes nothing.
//
// Thresholds read from grab-time sizes would behave differently: the adjacent
// segment's bump would assume the outer segments still held their grab sizes,
// so after a deep crush, clearing the red would mean retreating almost to the
// grab point and regrowing the pane far past its min.
type CrushPlan struct {
	// ASegs: segments before the boundary, adjacent-first; BSegs mirror
	// after the boundary. aRed/bRed is the sticky per-segment red state,
	// folded by Update.
	ASegs, BSegs []TreeNode
	aRed, bRed   []bool
}

// PlanCrush captures the corridor segments for target's boundary at arm. Red
// state starts empty, so a bare click closes nothing.
func PlanCrush(root TreeNode, rootRect Rect, target *Split, minPx float64) (CrushPlan, bool) {
	top, _, ok := corridorTop(root, rootRect, target)
	if !ok {
		return CrushPlan{}, false
	}
	topNode := TreeNode{Split: top}
	all := flattenCorridor(topNode, target.Dir)
	bIdx := boundaryIndex(all, target)
	if bIdx < 0 || bIdx+1 >= len(all) {
		return CrushPlan{}, false
	}
	var plan CrushPlan
	for i := bIdx; i >= 0; i-- {
		plan.ASegs = append(plan.ASegs, all[i])
	}
	plan.BSegs = append(plan.BSegs, all[bIdx+1:]...)
	plan.aRed = make([]bool, len(plan.ASegs))
	plan.bRed = make([]bool, len(plan.BSegs))
	return plan, true
}

// Update folds one cursor move into the red state, reading thresholds off the
// layout before the move is applied — call it before ResizeThrough. The
// pre-move read is the point: while a crushed segment rides the drag at its
// min, its live threshold tracks the boundary exactly, and only the pre-move
// snapshot can tell "pressed deeper", which stays red, from "backed off",
// which clears. crushEps makes each threshold a small sticky band, so
// sub-pixel jitter never flickers the verdict.
func (cp *CrushPlan) Update(root TreeNode, rootRect Rect, target *Split, minPx, cursorPx float64) {
	top, topRect, ok := corridorTop(root, rootRect, target)
	if !ok {
		return
	}
	topNode := TreeNode{Split: top}
	all := flattenCorridor(topNode, target.Dir)
	start, total := axisSpan(topRect, target.Dir)
	sizes := segmentSizes(topNode, target.Dir, total)
	bIdx := boundaryIndex(all, target)
	if bIdx < 0 {
		return
	}
	edges := make([]float64, len(all)+1)
	edges[0] = start
	for i, sz := range sizes {
		edges[i+1] = edges[i] + sz
	}
	for k := range cp.ASegs {
		i := bIdx - k
		th := edges[i] + minSize(all[i], target.Dir, minPx)
		switch {
		case cursorPx < th-crushEps:
			cp.aRed[k] = true
		case cursorPx > th+crushEps:
			cp.aRed[k] = false
		}
	}
	for k := range cp.BSegs {
		i := bIdx + 1 + k
		th := edges[i+1] - minSize(all[i], target.Dir, minPx)
		switch {
		case cursorPx > th+crushEps:
			cp.bRed[k] = true
		case cursorPx < th-crushEps:
			cp.bRed[k] = false
		}
	}
}

// Red returns the segments currently pressed to close — the contiguous
// boundary-adjacent run on the pressed side. The preview and the release
// both read this one state, so they cannot disagree.
func (cp *CrushPlan) Red() []TreeNode {
	var out []TreeNode
	for k, r := range cp.aRed {
		if !r {
			break
		}
		out = append(out, cp.ASegs[k])
	}
	if len(out) > 0 {
		return out
	}
	for k, r := range cp.bRed {
		if !r {
			break
		}
		out = append(out, cp.BSegs[k])
	}
	return out
}

// SegmentRects returns the live rects of the given corridor segments, for the
// red close overlay. They are recomputed from current sizes, so the overlay
// tracks the crush rather than a stale arm-time copy.
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

// HasSegment reports whether a subtree is still part of the tree. A corner
// grab arms one resize per axis, and one axis's crush verdict can close a
// subtree that contains the other's, so the release asks before flushing:
// a segment already gone must not be flushed or removed a second time.
func HasSegment(root TreeNode, seg TreeNode) bool {
	if root == seg {
		return true
	}
	if root.Split == nil {
		return false
	}
	return HasSegment(root.Split.A, seg) || HasSegment(root.Split.B, seg)
}

// LocateSplit returns target's current laid-out container rect within the
// tree. It must be the live geometry: a rect captured at arm time goes stale
// the moment a cascade moves an ancestor ratio.
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

// corridorTop returns target's topmost same-axis ancestor split (possibly
// target itself) and its laid-out rect: the corridor the boundary drag can
// reach. A perpendicular parent, or the root, ends the climb.
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
// not same-axis splits — the corridor segments.
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

// boundaryIndex finds the segment index the dragged boundary sits after: the
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
// minPx for a leaf, the sum of both children for a same-axis split, the max
// for a perpendicular one, whose children share the extent.
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
