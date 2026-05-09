package pane

import "testing"

func nearRect(t *testing.T, name string, got, want Rect) {
	t.Helper()
	const eps = 1e-9
	if absf(got.X-want.X) > eps || absf(got.Y-want.Y) > eps ||
		absf(got.W-want.W) > eps || absf(got.H-want.H) > eps {
		t.Errorf("%s: got %+v, want %+v", name, got, want)
	}
}

func absf(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func TestLayoutSinglePane(t *testing.T) {
	tr := NewTree()
	id := tr.FocusedPane().ID
	root := Rect{X: 0, Y: 0, W: 1024, H: 768}
	got := Layout(tr, root)
	if len(got) != 1 {
		t.Fatalf("want 1 entry, got %d", len(got))
	}
	nearRect(t, id, got[id], root)
}

func TestLayoutHorizontalSplit(t *testing.T) {
	tr := NewTree()
	a := tr.FocusedPane().ID
	bP, _ := tr.Split(Horizontal)
	b := bP.ID
	tr.SetRatio(a, 0.25)

	root := Rect{X: 0, Y: 0, W: 1000, H: 800}
	got := Layout(tr, root)
	nearRect(t, "a", got[a], Rect{X: 0, Y: 0, W: 1000, H: 200})
	nearRect(t, "b", got[b], Rect{X: 0, Y: 200, W: 1000, H: 600})
}

func TestLayoutVerticalSplit(t *testing.T) {
	tr := NewTree()
	a := tr.FocusedPane().ID
	bP, _ := tr.Split(Vertical)
	b := bP.ID
	tr.SetRatio(a, 0.4)

	root := Rect{X: 100, Y: 200, W: 500, H: 400}
	got := Layout(tr, root)
	nearRect(t, "a", got[a], Rect{X: 100, Y: 200, W: 200, H: 400})
	nearRect(t, "b", got[b], Rect{X: 300, Y: 200, W: 300, H: 400})
}

func TestLayoutNested(t *testing.T) {
	// Tree: H split with A=p1, B=(V split with A=p2, B=p3).
	tr := NewTree()
	a := tr.FocusedPane().ID
	bP, _ := tr.Split(Horizontal)
	b := bP.ID
	tr.SetRatio(a, 0.5)
	tr.SetFocus(b)
	cP, _ := tr.Split(Vertical)
	c := cP.ID
	tr.SetRatio(b, 0.5)

	root := Rect{X: 0, Y: 0, W: 1000, H: 1000}
	got := Layout(tr, root)
	if len(got) != 3 {
		t.Fatalf("want 3 entries, got %d", len(got))
	}
	nearRect(t, "a", got[a], Rect{X: 0, Y: 0, W: 1000, H: 500})
	nearRect(t, "b", got[b], Rect{X: 0, Y: 500, W: 500, H: 500})
	nearRect(t, "c", got[c], Rect{X: 500, Y: 500, W: 500, H: 500})
}

func TestDividersNoneForSinglePane(t *testing.T) {
	tr := NewTree()
	if got := Dividers(tr, Rect{W: 100, H: 100}, 4); len(got) != 0 {
		t.Errorf("want 0 dividers, got %d", len(got))
	}
}

func TestDividersOneSplit(t *testing.T) {
	tr := NewTree()
	a := tr.FocusedPane().ID
	_, _ = tr.Split(Horizontal)
	tr.SetRatio(a, 0.5)

	got := Dividers(tr, Rect{W: 1000, H: 800}, 6)
	if len(got) != 1 {
		t.Fatalf("want 1 divider, got %d", len(got))
	}
	d := got[0]
	if d.Dir != Horizontal {
		t.Errorf("dir = %v, want Horizontal", d.Dir)
	}
	// Divider band straddles the split line at y=400, 6px thick.
	nearRect(t, "div", d.Rect, Rect{X: 0, Y: 397, W: 1000, H: 6})
	// ContainerRect == root since this is the top-level split.
	nearRect(t, "container", d.ContainerRect, Rect{W: 1000, H: 800})
}

func TestDividersNested(t *testing.T) {
	tr := NewTree()
	a := tr.FocusedPane().ID
	bP, _ := tr.Split(Horizontal)
	b := bP.ID
	tr.SetRatio(a, 0.5)
	tr.SetFocus(b)
	_, _ = tr.Split(Vertical)
	tr.SetRatio(b, 0.5)

	got := Dividers(tr, Rect{W: 1000, H: 1000}, 4)
	if len(got) != 2 {
		t.Fatalf("want 2 dividers, got %d", len(got))
	}
	// Outer horizontal divider at y=500, full width.
	// Inner vertical divider at x=500, in the lower half.
	var sawH, sawV bool
	for _, d := range got {
		if d.Dir == Horizontal && d.Rect.W == 1000 {
			sawH = true
		}
		if d.Dir == Vertical && d.Rect.H == 500 {
			sawV = true
		}
	}
	if !sawH || !sawV {
		t.Errorf("expected one H + one V divider; got %+v", got)
	}
}

func TestRatioFromCursor(t *testing.T) {
	cont := Rect{X: 100, Y: 200, W: 400, H: 600}
	cases := []struct {
		name    string
		dir     Direction
		sx, sy  float64
		want    float64
	}{
		{"horizontal at top edge", Horizontal, 0, 200, 0},
		{"horizontal at bottom edge", Horizontal, 0, 800, 1},
		{"horizontal mid", Horizontal, 0, 500, 0.5},
		{"vertical at left edge", Vertical, 100, 0, 0},
		{"vertical at right edge", Vertical, 500, 0, 1},
		{"vertical mid", Vertical, 300, 0, 0.5},
		{"horizontal above container clamps to 0", Horizontal, 0, 0, 0},
		{"horizontal below container clamps to 1", Horizontal, 0, 9999, 1},
	}
	for _, c := range cases {
		got := RatioFromCursor(cont, c.dir, c.sx, c.sy)
		if absf(got-c.want) > 1e-9 {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRatioFromCursorZeroContainer(t *testing.T) {
	// Degenerate: zero-size container returns 0.5 (no information).
	if got := RatioFromCursor(Rect{}, Horizontal, 0, 0); got != 0.5 {
		t.Errorf("zero container: got %v, want 0.5", got)
	}
}

func TestDividersThicknessDefault(t *testing.T) {
	tr := NewTree()
	a := tr.FocusedPane().ID
	_, _ = tr.Split(Horizontal)
	tr.SetRatio(a, 0.5)
	got := Dividers(tr, Rect{W: 1000, H: 800}, 0) // 0 → default 4
	if got[0].Rect.H != 4 {
		t.Errorf("default thickness = %v, want 4", got[0].Rect.H)
	}
}
