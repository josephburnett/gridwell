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
