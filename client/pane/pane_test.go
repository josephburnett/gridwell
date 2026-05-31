package pane

import "testing"

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
	first.Path = []int64{1, 2, 3}
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
	first.Path[0] = 99
	if newP.Path[0] == 99 {
		t.Error("path was shallow-copied")
	}
}

func TestCloseLastPaneRefused(t *testing.T) {
	tr := NewTree()
	if err := tr.Close(); err == nil {
		t.Error("expected error closing last pane")
	}
}

func TestSplitAndClose(t *testing.T) {
	tr := NewTree()
	first := tr.FocusedPane().ID
	if _, err := tr.Split(Vertical); err != nil {
		t.Fatal(err)
	}
	// Focus is still on first.
	if tr.Focus != first {
		t.Errorf("focus shifted: %q", tr.Focus)
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	if tr.Count() != 1 {
		t.Errorf("count after close = %d", tr.Count())
	}
}

func TestNestedSplitsAndClose(t *testing.T) {
	tr := NewTree()
	a := tr.FocusedPane().ID
	b, err := tr.Split(Horizontal)
	if err != nil {
		t.Fatal(err)
	}
	_ = tr.SetFocus(b.ID)
	c, err := tr.Split(Vertical)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Count() != 3 {
		t.Errorf("count = %d", tr.Count())
	}
	// Close c; b should remain.
	_ = tr.SetFocus(c.ID)
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	if tr.FocusedPane().ID != b.ID {
		t.Errorf("focus after close = %q, want %q", tr.Focus, b.ID)
	}
	if tr.FindPane(a) == nil {
		t.Error("pane a got dropped")
	}
}

func TestSetRatio(t *testing.T) {
	tr := NewTree()
	a := tr.FocusedPane().ID
	b, _ := tr.Split(Horizontal)

	if !tr.SetRatio(a, 0.3) {
		t.Error("SetRatio returned false for valid pane")
	}
	// Walk to find the split's ratio.
	if tr.Root.Split.Ratio != 0.3 {
		t.Errorf("ratio = %v", tr.Root.Split.Ratio)
	}
	// Clamping.
	if !tr.SetRatio(b.ID, 1.5) {
		t.Error("SetRatio returned false")
	}
	if tr.Root.Split.Ratio != 1.0 {
		t.Errorf("clamp upper bound: ratio = %v", tr.Root.Split.Ratio)
	}
	if !tr.SetRatio(b.ID, -0.5) {
		t.Error("SetRatio returned false")
	}
	if tr.Root.Split.Ratio != 0.0 {
		t.Errorf("clamp lower bound: ratio = %v", tr.Root.Split.Ratio)
	}
}

func TestTruncatePathTo(t *testing.T) {
	known := map[int64]bool{1: true, 2: true, 3: true}
	cases := []struct {
		in   []int64
		want []int64
	}{
		{nil, nil},
		{[]int64{}, nil},
		{[]int64{1, 2, 3}, []int64{1, 2, 3}},
		{[]int64{1, 2, 99}, []int64{1, 2}},
		{[]int64{1, 99, 99}, []int64{1}},
		{[]int64{99, 99, 99}, nil},
		{[]int64{99, 1}, []int64{99, 1}}, // trailing-valid: keeps everything since the deepest is valid
	}
	for _, c := range cases {
		got := TruncatePathTo(c.in, known)
		if !sliceEqual(got, c.want) {
			t.Errorf("TruncatePathTo(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestSetFocusUnknown returns an error.
func TestSetFocusUnknown(t *testing.T) {
	tr := NewTree()
	if err := tr.SetFocus("nope"); err == nil {
		t.Error("expected error")
	}
}

func sliceEqual(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestCloneCarriesTextFields(t *testing.T) {
	src := &Pane{
		ID:          "p1",
		Path:        []int64{1, 2},
		Cx:          3, Cy: 4, Zoom: 5,
		TextFocus:   42,
		TextMode:    "text",
		TextScrollX: 1.5,
		TextScrollY: 7.25,
		TextZoom:    1.1,
	}
	dst := src.Clone("p2")
	if dst.TextFocus != 42 || dst.TextMode != "text" {
		t.Errorf("text focus/mode not cloned: %+v", dst)
	}
	if dst.TextScrollX != 1.5 || dst.TextScrollY != 7.25 || dst.TextZoom != 1.1 {
		t.Errorf("text scroll/zoom not cloned: %+v", dst)
	}
	// And changing the source post-clone shouldn't bleed through.
	src.TextFocus = 99
	if dst.TextFocus == 99 {
		t.Error("clone shares TextFocus with source")
	}
}

func TestSplitInheritsTextFields(t *testing.T) {
	tr := NewTree()
	first := tr.FocusedPane()
	first.TextFocus = 77
	first.TextMode = "rendered"
	first.TextScrollY = 12.5
	first.TextZoom = 0.85

	newP, err := tr.Split(Horizontal)
	if err != nil {
		t.Fatal(err)
	}
	if newP.TextFocus != 77 || newP.TextMode != "rendered" {
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
		newP, err := tr.SplitOnSide(c.side)
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
		{SideTop, -0.5, 0.0},  // clamps below
		{SideTop, 1.5, 1.0},   // clamps above
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
	tr := NewTree()
	a := tr.FocusedPane().ID
	bP, _ := tr.Split(Horizontal)
	b := bP.ID

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
	tr := NewTree()
	a := tr.FocusedPane().ID
	bP, _ := tr.Split(Horizontal)
	b := bP.ID
	_ = tr.SetFocus(b)
	cP, _ := tr.Split(Vertical)
	c := cP.ID
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

func TestCollapseSplitDropA(t *testing.T) {
	tr := NewTree()
	a := tr.FocusedPane().ID
	bP, _ := tr.Split(Horizontal)
	b := bP.ID
	if err := tr.CollapseSplit(tr.Root.Split, true); err != nil {
		t.Fatal(err)
	}
	if tr.Count() != 1 {
		t.Errorf("count = %d, want 1", tr.Count())
	}
	if tr.FindPane(b) == nil {
		t.Error("surviving pane b missing")
	}
	if tr.FindPane(a) != nil {
		t.Error("dropped pane a still present")
	}
	if tr.Focus != b {
		t.Errorf("focus = %q, want %q", tr.Focus, b)
	}
}

func TestCollapseSplitDropB(t *testing.T) {
	tr := NewTree()
	a := tr.FocusedPane().ID
	bP, _ := tr.Split(Horizontal)
	b := bP.ID
	if err := tr.CollapseSplit(tr.Root.Split, false); err != nil {
		t.Fatal(err)
	}
	if tr.FindPane(a) == nil {
		t.Error("surviving pane a missing")
	}
	if tr.FindPane(b) != nil {
		t.Error("dropped pane b still present")
	}
	if tr.Focus != a {
		t.Errorf("focus = %q, want %q", tr.Focus, a)
	}
}

func TestCollapseSplitNested(t *testing.T) {
	// root: H (A=a, B=split V (A=b, B=c))
	tr := NewTree()
	a := tr.FocusedPane().ID
	bP, _ := tr.Split(Horizontal)
	b := bP.ID
	tr.SetFocus(b)
	cP, _ := tr.Split(Vertical)
	c := cP.ID

	// Collapse the OUTER split, dropping A (pane a).
	// Surviving subtree is the inner V split (still containing b, c).
	if err := tr.CollapseSplit(tr.Root.Split, true); err != nil {
		t.Fatal(err)
	}
	if tr.Count() != 2 {
		t.Errorf("count = %d, want 2", tr.Count())
	}
	if tr.FindPane(b) == nil || tr.FindPane(c) == nil {
		t.Error("inner panes lost")
	}
	if tr.FindPane(a) != nil {
		t.Error("pane a still present")
	}
}

// TestPropertyAtLeastOnePane runs a random sequence of split/close operations
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
			_ = tr.Close()
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
