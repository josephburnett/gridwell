package pane

import "testing"

// Count is the tests' pane tally. Test-only — production walks the tree, it
// never counts (the deadcode gate keeps it that way).
func (t *Tree) Count() int {
	n := 0
	t.Walk(func(*Pane) { n++ })
	return n
}

// twoPaneTree returns a new Tree with two panes: the original (a) and a
// horizontal sibling (b). Focus remains on a.
func twoPaneTree(t *testing.T) (tr *Tree, a, b string) {
	t.Helper()
	tr = NewTree()
	a = tr.FocusedPane().ID
	bP, err := tr.Split(Horizontal)
	if err != nil {
		t.Fatalf("twoPaneTree: split: %v", err)
	}
	return tr, a, bP.ID
}

// threePaneTree returns a new Tree with three panes: a (original), b
// (horizontal sibling of a), and c (vertical sibling of b, focused).
func threePaneTree(t *testing.T) (tr *Tree, a, b, c string) {
	t.Helper()
	tr, a, b = twoPaneTree(t)
	_ = tr.SetFocus(b)
	cP, err := tr.Split(Vertical)
	if err != nil {
		t.Fatalf("threePaneTree: split: %v", err)
	}
	return tr, a, b, cP.ID
}

func TestNewTreeHasOneFocusedPane(t *testing.T) {
	tr := NewTree()
	if tr.Count() != 1 {
		t.Errorf("count = %d", tr.Count())
	}
	if tr.FocusedPane() == nil {
		t.Error("focused pane nil")
	}
	if tr.FocusedPane().ID != tr.Focus {
		t.Error("focused id mismatch")
	}
}

func TestSplitHorizontalCreatesSibling(t *testing.T) {
	tr := NewTree()
	first := tr.FocusedPane()
	first.Path = []string{"1", "2", "3"}
	first.Cx, first.Cy, first.Zoom = 10, 20, 2.0

	newP, err := tr.Split(Horizontal)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Count() != 2 {
		t.Errorf("count = %d", tr.Count())
	}
	if newP.ID == first.ID {
		t.Error("new pane has same id as old")
	}
	// Clone preserves state.
	if newP.Cx != 10 || newP.Cy != 20 || newP.Zoom != 2.0 {
		t.Errorf("new pane state not cloned: %+v", newP)
	}
	if len(newP.Path) != 3 {
		t.Errorf("path not cloned: %+v", newP.Path)
	}
	// Mutating the original should not affect the clone (deep copy).
	first.Path[0] = "99"
	if newP.Path[0] == "99" {
		t.Error("path was shallow-copied")
	}
}

// TestSetFocusUnknown returns an error.
func TestSetFocusUnknown(t *testing.T) {
	tr := NewTree()
	if err := tr.SetFocus("nope"); err == nil {
		t.Error("expected error")
	}
}

func TestCloneCarriesTextFields(t *testing.T) {
	src := &Pane{
		ID:   "p1",
		Path: []string{"1", "2"},
		Cx:   3, Cy: 4, Zoom: 5,
		TextFocus:   "42",
		TextMode:    "text",
		TextScrollX: 1.5,
		TextScrollY: 7.25,
		TextZoom:    1.1,
	}
	dst := src.Clone("p2")
	if dst.TextFocus != "42" || dst.TextMode != "text" {
		t.Errorf("text focus/mode not cloned: %+v", dst)
	}
	if dst.TextScrollX != 1.5 || dst.TextScrollY != 7.25 || dst.TextZoom != 1.1 {
		t.Errorf("text scroll/zoom not cloned: %+v", dst)
	}
	// And changing the source post-clone shouldn't bleed through.
	src.TextFocus = "99"
	if dst.TextFocus == "99" {
		t.Error("clone shares TextFocus with source")
	}
}

// TestPortalStackRoundTrip: entering a plugin pushes the current level onto
// the Up stack; ascending pops it back, restoring the exact level — anchor,
// path, viewport, and text focus. STACK semantics: ascend returns where you
// were.
func TestPortalStackRoundTrip(t *testing.T) {
	p := &Pane{
		ID:     "p1",
		Anchor: "db-uuid/1",
		Path:   []string{"3", "4"},
		Cx:     5, Cy: 6, Zoom: 1.5,
		TextFocus: "9", TextMode: "text", TextScrollY: 2.0, TextZoom: 1.2,
	}
	p.PushFrame(true) // menu was open when we entered
	if f, ok := p.TopFrame(); !ok || !f.MenuOpen {
		t.Errorf("TopFrame MenuOpen = %v, want true", f.MenuOpen)
	}
	// Jump into another plugin at its root.
	p.Anchor = "fs-uuid/1"
	p.Path = nil
	p.Cx, p.Cy, p.Zoom = 0, 0, 1
	p.TextFocus = ""

	if !p.PopFrame() {
		t.Fatal("PopFrame returned false with a frame on the stack")
	}
	if p.Anchor != "db-uuid/1" {
		t.Errorf("anchor = %q, want db-uuid/1", p.Anchor)
	}
	if len(p.Path) != 2 || p.Path[0] != "3" || p.Path[1] != "4" {
		t.Errorf("path = %v, want [3 4]", p.Path)
	}
	if p.Cx != 5 || p.Cy != 6 || p.Zoom != 1.5 {
		t.Errorf("viewport = (%v,%v,%v), want (5,6,1.5)", p.Cx, p.Cy, p.Zoom)
	}
	if p.TextFocus != "9" || p.TextMode != "text" {
		t.Errorf("text focus/mode not restored: %+v", p)
	}
	// Stack is now empty: nothing left to ascend to.
	if p.PopFrame() {
		t.Error("PopFrame returned true on an empty stack")
	}
}

// TestDropFrameRemovesWithoutApplying: an animated portal ascent drops the
// frame (so it can't be ascended to twice) but leaves the pane's live state
// alone — the transition, not the pop, drives the pane back to that viewport.
func TestDropFrameRemovesWithoutApplying(t *testing.T) {
	p := &Pane{ID: "p1", Anchor: "fs-uuid/1", Cx: 9, Cy: 9, Zoom: 2}
	p.Up = []Frame{{Anchor: "", Cx: 1, Cy: 2, Zoom: 1}}

	if !p.DropFrame() {
		t.Fatal("DropFrame returned false with a frame on the stack")
	}
	if len(p.Up) != 0 {
		t.Errorf("Up len = %d, want 0", len(p.Up))
	}
	// Live state is untouched — unlike PopFrame, DropFrame does not restore.
	if p.Anchor != "fs-uuid/1" || p.Cx != 9 || p.Cy != 9 || p.Zoom != 2 {
		t.Errorf("DropFrame mutated live state: %+v", p)
	}
	if p.DropFrame() {
		t.Error("DropFrame returned true on an empty stack")
	}
}

// TestCloneDeepCopiesUpStack: a clone must not share the Up stack (or its
// frame paths) with the source, else editing one pane's nav history mutates
// the other's.
func TestCloneDeepCopiesUpStack(t *testing.T) {
	src := &Pane{ID: "p1", Anchor: "fs-uuid/1"}
	src.Up = []Frame{{Anchor: "db-uuid/1", Path: []string{"3", "4"}}}
	dst := src.Clone("p2")
	// Mutate the source frame's path in place.
	src.Up[0].Path[0] = "99"
	if dst.Up[0].Path[0] != "3" {
		t.Error("clone shares Up frame path slice with source")
	}
}

func TestSplitInheritsTextFields(t *testing.T) {
	tr := NewTree()
	first := tr.FocusedPane()
	first.TextFocus = "77"
	first.TextMode = "rendered"
	first.TextScrollY = 12.5
	first.TextZoom = 0.85

	newP, err := tr.Split(Horizontal)
	if err != nil {
		t.Fatal(err)
	}
	if newP.TextFocus != "77" || newP.TextMode != "rendered" {
		t.Errorf("text fields not inherited: %+v", newP)
	}
	if newP.TextScrollY != 12.5 || newP.TextZoom != 0.85 {
		t.Errorf("scroll/zoom not inherited: %+v", newP)
	}
}

func TestSplitOnSideTopAndBottom(t *testing.T) {
	cases := []struct {
		side       Side
		wantDir    Direction
		wantAIsNew bool // true = new pane in A, existing in B
	}{
		{SideTop, Horizontal, true},
		{SideBottom, Horizontal, false},
		{SideLeft, Vertical, true},
		{SideRight, Vertical, false},
	}
	for _, c := range cases {
		tr := NewTree()
		first := tr.FocusedPane().ID
		newP, err := tr.SplitOnSideAt(c.side, 0.5)
		if err != nil {
			t.Fatalf("side %v: %v", c.side, err)
		}
		if tr.Root.Split == nil {
			t.Fatalf("side %v: root not a split", c.side)
		}
		if tr.Root.Split.Dir != c.wantDir {
			t.Errorf("side %v: dir = %v, want %v", c.side, tr.Root.Split.Dir, c.wantDir)
		}
		var aID, bID string
		if tr.Root.Split.A.IsLeaf() {
			aID = tr.Root.Split.A.Pane.ID
		}
		if tr.Root.Split.B.IsLeaf() {
			bID = tr.Root.Split.B.Pane.ID
		}
		if c.wantAIsNew {
			if aID != newP.ID {
				t.Errorf("side %v: A=%q want new=%q", c.side, aID, newP.ID)
			}
			if bID != first {
				t.Errorf("side %v: B=%q want existing=%q", c.side, bID, first)
			}
		} else {
			if aID != first {
				t.Errorf("side %v: A=%q want existing=%q", c.side, aID, first)
			}
			if bID != newP.ID {
				t.Errorf("side %v: B=%q want new=%q", c.side, bID, newP.ID)
			}
		}
		if tr.Focus != newP.ID {
			t.Errorf("side %v: focus = %q, want new %q", c.side, tr.Focus, newP.ID)
		}
	}
}

func TestSplitOnSideAtRatio(t *testing.T) {
	cases := []struct {
		side  Side
		ratio float64
		// What ratio should the resulting Split.Ratio be?
		// SideTop/SideLeft: new pane in A, so split.Ratio = ratio.
		// SideBottom/SideRight: new pane in B, so split.Ratio = 1 - ratio.
		wantSplit float64
	}{
		{SideTop, 0.3, 0.3},
		{SideLeft, 0.4, 0.4},
		{SideBottom, 0.3, 0.7},
		{SideRight, 0.25, 0.75},
		{SideTop, -0.5, 0.0}, // clamps below
		{SideTop, 1.5, 1.0},  // clamps above
		{SideBottom, -1.0, 1.0},
		{SideBottom, 5.0, 0.0},
	}
	for _, c := range cases {
		tr := NewTree()
		newP, err := tr.SplitOnSideAt(c.side, c.ratio)
		if err != nil {
			t.Fatalf("side=%v ratio=%v: %v", c.side, c.ratio, err)
		}
		got := tr.Root.Split.Ratio
		if absDiff(got, c.wantSplit) > 1e-9 {
			t.Errorf("side=%v ratio=%v: split.Ratio = %v, want %v",
				c.side, c.ratio, got, c.wantSplit)
		}
		if tr.Focus != newP.ID {
			t.Errorf("side=%v ratio=%v: focus = %q, want new %q",
				c.side, c.ratio, tr.Focus, newP.ID)
		}
	}
}

func absDiff(a, b float64) float64 {
	if a > b {
		return a - b
	}
	return b - a
}

func TestSwapBasic(t *testing.T) {
	tr, a, b := twoPaneTree(t)

	// Pre-swap: A is in slot Split.A, B is in slot Split.B.
	if tr.Root.Split.A.Pane.ID != a || tr.Root.Split.B.Pane.ID != b {
		t.Fatalf("pre-swap shape unexpected")
	}
	if err := tr.Swap(a, b); err != nil {
		t.Fatal(err)
	}
	if tr.Root.Split.A.Pane.ID != b || tr.Root.Split.B.Pane.ID != a {
		t.Errorf("post-swap shape: A=%q B=%q", tr.Root.Split.A.Pane.ID, tr.Root.Split.B.Pane.ID)
	}
	// Both ids still resolvable.
	if tr.FindPane(a) == nil || tr.FindPane(b) == nil {
		t.Error("a pane disappeared after swap")
	}
	// Swap does not move focus.
	if tr.Focus != a {
		t.Errorf("focus moved after swap: %q want %q", tr.Focus, a)
	}
}

func TestSwapSelfNoop(t *testing.T) {
	tr := NewTree()
	a := tr.FocusedPane().ID
	if err := tr.Swap(a, a); err != nil {
		t.Errorf("self-swap returned error: %v", err)
	}
}

func TestSwapUnknownIDError(t *testing.T) {
	tr := NewTree()
	a := tr.FocusedPane().ID
	_, _ = tr.Split(Horizontal)
	if err := tr.Swap(a, "ghost"); err == nil {
		t.Error("expected error for unknown id")
	}
}

func TestSwapInDeepTree(t *testing.T) {
	tr, a, b, c := threePaneTree(t)
	// Tree shape:
	//   root: H (A=a, B=split V (A=b, B=c))
	if err := tr.Swap(a, c); err != nil {
		t.Fatal(err)
	}
	// After swap a↔c: root.A should be c, and the inner V's B should be a.
	if tr.Root.Split.A.Pane.ID != c {
		t.Errorf("root.A = %q, want %q", tr.Root.Split.A.Pane.ID, c)
	}
	innerB := tr.Root.Split.B
	if innerB.IsLeaf() || innerB.Split.B.Pane.ID != a {
		t.Errorf("inner.B = %v, want pane %q", innerB, a)
	}
	// b stayed in place inside the inner split's A.
	if innerB.Split.A.Pane.ID != b {
		t.Errorf("inner.A = %q, want %q", innerB.Split.A.Pane.ID, b)
	}
}

// TestPropertyAtLeastOnePane runs a random sequence of split/close operations
// (close = RemoveSegment on the focused leaf, the live crush-close mechanism)
// and asserts that the pane count never drops below 1.
func TestPropertyAtLeastOnePane(t *testing.T) {
	tr := NewTree()
	ops := []string{"split-h", "split-v", "close", "focus-other"}
	// Deterministic pseudo-random sequence.
	seq := []int{0, 1, 2, 0, 1, 0, 2, 1, 2, 3, 0, 2, 0, 0, 2, 1, 2}
	for _, opIdx := range seq {
		op := ops[opIdx%len(ops)]
		switch op {
		case "split-h":
			_, _ = tr.Split(Horizontal)
		case "split-v":
			_, _ = tr.Split(Vertical)
		case "close":
			if h := findPaneNode(&tr.Root, tr.Focus); h != nil {
				_ = tr.RemoveSegment(*h)
			}
		case "focus-other":
			tr.Walk(func(p *Pane) {
				if p.ID != tr.Focus {
					_ = tr.SetFocus(p.ID)
				}
			})
		}
		if tr.Count() < 1 {
			t.Fatalf("count dropped below 1 after %s", op)
		}
		if tr.FocusedPane() == nil {
			t.Fatalf("focused pane nil after %s", op)
		}
	}
}

func TestStillDescended(t *testing.T) {
	p := &Pane{ID: "p", TextFocus: "u1/7"}
	if !StillDescended(p, "u1/7") {
		t.Fatal("descended pane must still count")
	}
	if StillDescended(nil, "u1/7") || StillDescended(p, "u1/8") || StillDescended(&Pane{ID: "p"}, "u1/7") {
		t.Fatal("closed, moved on, or ascended must not")
	}
}
