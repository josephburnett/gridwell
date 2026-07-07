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

// ResizeThrough moves target's divider toward cursorPx (a coordinate along
// target.Dir's axis), cascading compression through the corridor with each
// leaf pane clamped to minPx. root/rootRect are the whole tree, so the
// corridor can cross target's same-axis ancestors.
func ResizeThrough(root TreeNode, rootRect Rect, target *Split, cursorPx, minPx float64) {
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

	// The min-size walls on each side of the boundary.
	lo := start
	for _, s := range segs[:bIdx+1] {
		lo += minSize(s, target.Dir, minPx)
	}
	hi := start + total
	for _, s := range segs[bIdx+1:] {
		hi -= minSize(s, target.Dir, minPx)
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
