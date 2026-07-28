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

// Issue #80: tmux-like pane zoom. Zoomed, the leaf owns the whole root rect
// and every other pane vanishes from the layout (their live views park via
// the missing-rect path); dividers vanish with them so no gesture can arm on
// an invisible boundary. Structural edits unzoom first. Unzoom restores the
// exact prior layout — the split ratios were never touched.
func TestZoomLayout(t *testing.T) {
	tr := NewTree()
	p2, err := tr.Split(Vertical)
	if err != nil {
		t.Fatal(err)
	}
	root := Rect{X: 0, Y: 0, W: 400, H: 200}
	before := Layout(tr, root)

	tr.ToggleZoom(p2.ID)
	zoomed := Layout(tr, root)
	if len(zoomed) != 1 {
		t.Fatalf("zoomed layout has %d panes, want 1", len(zoomed))
	}
	if r := zoomed[p2.ID]; r != root {
		t.Errorf("zoomed pane rect = %+v, want the whole root", r)
	}
	if divs := Dividers(tr, root, 10); len(divs) != 0 {
		t.Errorf("dividers while zoomed = %d, want 0", len(divs))
	}

	tr.ToggleZoom(p2.ID) // toggle back
	after := Layout(tr, root)
	if len(after) != len(before) {
		t.Fatalf("unzoom pane count = %d, want %d", len(after), len(before))
	}
	for id, r := range before {
		if after[id] != r {
			t.Errorf("pane %s rect changed across zoom round trip: %+v -> %+v", id, r, after[id])
		}
	}
}

func TestZoomClearedByStructuralEdits(t *testing.T) {
	tr := NewTree()
	p2, _ := tr.Split(Vertical)
	tr.ToggleZoom(p2.ID)
	if _, err := tr.Split(Horizontal); err != nil {
		t.Fatal(err)
	}
	if tr.Zoomed != "" {
		t.Error("Split must unzoom first")
	}

	tr2 := NewTree()
	q2, _ := tr2.Split(Vertical)
	tr2.ToggleZoom(q2.ID)
	if err := tr2.Swap(tr2.Root.Split.A.Pane.ID, q2.ID); err != nil {
		t.Fatal(err)
	}
	if tr2.Zoomed != "" {
		t.Error("Swap must unzoom first")
	}
}

func TestZoomUnknownPaneIsNoOp(t *testing.T) {
	tr := NewTree()
	tr.ToggleZoom("nope")
	if tr.Zoomed != "" {
		t.Errorf("Zoomed = %q, want unset for an unknown pane", tr.Zoomed)
	}
	// A stale Zoomed id (pane collapsed away) must not blank the layout.
	tr.Zoomed = "gone"
	l := Layout(tr, Rect{W: 100, H: 100})
	if len(l) != 1 {
		t.Errorf("layout with stale zoom = %d panes, want the normal 1", len(l))
	}
}

// TestCorridorWalls pins the one wall (issue: the 2026-07-27 stale-container
// close). For the INNER divider of stack3 (middle/bottom boundary), the
// corridor spans the WHOLE 300px column: lo must clear top+middle at min
// (2×32 from the corridor start), not the inner split's own container edge
// (y=100) — the difference is exactly where the false mid-corridor collapse
// lived. And the walls must equal what ResizeThrough actually clamps to.
func TestCorridorSpan(t *testing.T) {
	outer, inner := stack3()
	root := TreeNode{Split: outer}
	container := Rect{X: 0, Y: 0, W: 100, H: 300}

	start, end, ok := CorridorSpan(root, container, inner)
	if !ok {
		t.Fatal("span not found for the inner divider")
	}
	if !near(start, 0) || !near(end, 300) {
		t.Errorf("span = [%v, %v], want [0, 300] (the whole same-axis column, not the inner container)", start, end)
	}

	// The span is invariant under the drag itself: the cascade moves
	// same-axis ratios, never the perpendicular ancestors that fix the span
	// — so the close threshold (issue #204) cannot go stale mid-drag.
	ResizeThrough(root, container, inner, 10, 32)
	s2, e2, ok := CorridorSpan(root, container, inner)
	if !ok || !near(s2, start) || !near(e2, end) {
		t.Errorf("span moved under the drag: [%v, %v] -> [%v, %v]", start, end, s2, e2)
	}
}

func TestCorridorWalls(t *testing.T) {
	outer, inner := stack3()
	root := TreeNode{Split: outer}
	container := Rect{X: 0, Y: 0, W: 100, H: 300}

	lo, hi, ok := CorridorWalls(root, container, inner, 32)
	if !ok {
		t.Fatal("walls not found for the inner divider")
	}
	if !near(lo, 64) {
		t.Errorf("lo = %v, want 64 (top+middle at min from the CORRIDOR start, not the inner container's y=100)", lo)
	}
	if !near(hi, 268) {
		t.Errorf("hi = %v, want 268 (bottom at min from the corridor end)", hi)
	}

	// The walls ARE the clamp: dragging beyond them applies exactly the wall.
	ResizeThrough(root, container, inner, 10, 32) // far past lo
	h := paneHeights(root, container)
	if !near(h["top"], 32) || !near(h["middle"], 32) {
		t.Errorf("clamped drag: top=%v middle=%v, want both at the 32 wall", h["top"], h["middle"])
	}

	// The outer divider's corridor is the same column; only the boundary
	// index moves: lo clears just top, hi clears middle+bottom.
	outer2, _ := stack3()
	lo2, hi2, ok := CorridorWalls(TreeNode{Split: outer2}, container, outer2, 32)
	if !ok || !near(lo2, 32) || !near(hi2, 236) {
		t.Errorf("outer walls = %v..%v (ok=%v), want 32..236", lo2, hi2, ok)
	}
}

// TestLocateSplit pins the live-geometry read the drag preview uses: the
// inner split's rect must reflect CURRENT ratios, not any captured copy.
func TestLocateSplit(t *testing.T) {
	outer, inner := stack3()
	root := TreeNode{Split: outer}
	container := Rect{X: 0, Y: 0, W: 100, H: 300}

	r, ok := LocateSplit(root, container, inner)
	if !ok || !near(r.Y, 100) || !near(r.H, 200) {
		t.Fatalf("inner rect = %+v (ok=%v), want y=100 h=200", r, ok)
	}
	// A cascade moves the outer ratio; the located rect must follow.
	ResizeThrough(root, container, inner, 64, 32)
	r, ok = LocateSplit(root, container, inner)
	if !ok || !near(r.Y, 32) {
		t.Fatalf("after cascade: inner rect = %+v (ok=%v), want y=32 (top crushed to min)", r, ok)
	}
}
