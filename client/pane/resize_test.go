package pane

import (
	"math"
	"testing"
)

// Issue #79: dragging a divider must cascade — the pane adjacent to the
// divider compresses to its minimum first, then the drag starts compressing
// the NEXT pane along the axis, tmux-style. The old single-ratio clamp
// squashed the whole opposite subtree proportionally and stopped at 32px
// TOTAL for the side, whatever it contained.

// stack3 builds three panes stacked vertically (two nested Horizontal
// splits): top = A of the outer split, middle/bottom nested in B.
//
//	outer: Split{Dir: Horizontal, Ratio: 1/3, A: top, B: inner}
//	inner: Split{Dir: Horizontal, Ratio: 1/2, A: middle, B: bottom}
func stack3() (outer, inner *Split) {
	top := &Pane{ID: "top"}
	middle := &Pane{ID: "middle"}
	bottom := &Pane{ID: "bottom"}
	inner = &Split{Dir: Horizontal, Ratio: 0.5, A: TreeNode{Pane: middle}, B: TreeNode{Pane: bottom}}
	outer = &Split{Dir: Horizontal, Ratio: 1.0 / 3.0, A: TreeNode{Pane: top}, B: TreeNode{Split: inner}}
	return outer, inner
}

// paneHeights lays out the given root over container and returns each leaf's
// height by id.
func paneHeights(root TreeNode, container Rect) map[string]float64 {
	out := map[string]float64{}
	var walk func(n TreeNode, r Rect)
	walk = func(n TreeNode, r Rect) {
		if n.IsLeaf() {
			out[n.Pane.ID] = r.H
			return
		}
		a, b := SplitRect(r, n.Split.Dir, n.Split.Ratio)
		walk(n.Split.A, a)
		walk(n.Split.B, b)
	}
	walk(root, container)
	return out
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.01 }

func TestResizeThroughCascades(t *testing.T) {
	// 300px tall, three 100px panes. Drag the outer divider (top/middle
	// boundary, at y=100) down to y=250: top grows to 250; the remaining 50px
	// must be MIDDLE at its 32px min and bottom keeping the rest (18px would
	// breach bottom's min, so the clamp stops earlier — at 300-32-32=236).
	c := Rect{X: 0, Y: 0, W: 100, H: 300}
	outer, _ := stack3()
	ResizeThrough(TreeNode{Split: outer}, c, outer, 250, 32)
	h := paneHeights(TreeNode{Split: outer}, c)
	if !near(h["top"], 236) {
		t.Errorf("top = %v, want 236 (clamped by the two 32px mins)", h["top"])
	}
	if !near(h["middle"], 32) {
		t.Errorf("middle = %v, want its 32px min", h["middle"])
	}
	if !near(h["bottom"], 32) {
		t.Errorf("bottom = %v, want its 32px min", h["bottom"])
	}
}

func TestResizeThroughCompressesAdjacentFirst(t *testing.T) {
	// A smaller drag (to y=150): only the ADJACENT pane (middle) shrinks —
	// 100→50 — and bottom is untouched. The old proportional squash would
	// have taken 25px from each.
	c := Rect{X: 0, Y: 0, W: 100, H: 300}
	outer, _ := stack3()
	ResizeThrough(TreeNode{Split: outer}, c, outer, 150, 32)
	h := paneHeights(TreeNode{Split: outer}, c)
	if !near(h["top"], 150) {
		t.Errorf("top = %v, want 150", h["top"])
	}
	if !near(h["middle"], 50) {
		t.Errorf("middle = %v, want 50 (adjacent compresses first)", h["middle"])
	}
	if !near(h["bottom"], 100) {
		t.Errorf("bottom = %v, want 100 (untouched)", h["bottom"])
	}
}

func TestResizeThroughInnerDividerCascadesUpward(t *testing.T) {
	// Drag the INNER divider (middle/bottom boundary at y=200) UP to y=40:
	// middle compresses to its 32px min... and the cascade must cross into
	// the OUTER split, shrinking top too. Travel wall: top>=32, middle>=32 →
	// boundary can reach 64.
	c := Rect{X: 0, Y: 0, W: 100, H: 300}
	outer, inner := stack3()
	ResizeThrough(TreeNode{Split: outer}, c, inner, 40, 32)
	h := paneHeights(TreeNode{Split: outer}, c)
	if !near(h["top"], 32) {
		t.Errorf("top = %v, want its 32px min (cascade crossed the outer split)", h["top"])
	}
	if !near(h["middle"], 32) {
		t.Errorf("middle = %v, want its 32px min", h["middle"])
	}
	if !near(h["bottom"], 236) {
		t.Errorf("bottom = %v, want the rest (236)", h["bottom"])
	}
}

func TestResizeThroughGrowGivesAdjacent(t *testing.T) {
	// Dragging AWAY (divider down 100→160 shrinks nothing on the growing
	// side): top grows; on the shrinking side only middle compresses.
	// Perpendicular splits inside are only re-scaled, never re-ratioed.
	left := &Pane{ID: "left"}
	right := &Pane{ID: "right"}
	perp := &Split{Dir: Vertical, Ratio: 0.25, A: TreeNode{Pane: left}, B: TreeNode{Pane: right}}
	top := &Pane{ID: "top"}
	outer := &Split{Dir: Horizontal, Ratio: 0.5, A: TreeNode{Pane: top}, B: TreeNode{Split: perp}}
	c := Rect{X: 0, Y: 0, W: 400, H: 200}
	ResizeThrough(TreeNode{Split: outer}, c, outer, 160, 32)
	if !near(outer.Ratio, 0.8) {
		t.Errorf("outer ratio = %v, want 0.8", outer.Ratio)
	}
	if !near(perp.Ratio, 0.25) {
		t.Errorf("perpendicular split ratio changed: %v", perp.Ratio)
	}
}
