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

func TestClassifyRegionLargePane(t *testing.T) {
	r := Rect{X: 0, Y: 0, W: 100, H: 100}
	cases := []struct {
		name   string
		x, y   float64
		want   Region
	}{
		{"top edge", 50, 5, RegionResizeTop},
		{"bottom edge", 50, 95, RegionResizeBottom},
		{"left edge", 5, 50, RegionResizeLeft},
		{"right edge", 95, 50, RegionResizeRight},
		{"corner top-left → top (tiebreak)", 5, 5, RegionResizeTop},
		{"corner bottom-right → bottom", 95, 95, RegionResizeBottom},
		{"dead center → swap", 50, 50, RegionSwap},
		{"upper-mid in swap zone", 50, 40, RegionSwap},
		{"upper-mid below swap → split top", 50, 32, RegionSplitTop},
		{"split top wide", 30, 25, RegionSplitTop},
		{"split right", 80, 50, RegionSplitRight},
		{"split bottom", 50, 70, RegionSplitBottom},
		{"split left", 20, 50, RegionSplitLeft},
	}
	for _, c := range cases {
		got := ClassifyRegion(r, 10, c.x, c.y)
		if got != c.want {
			t.Errorf("%s @(%v,%v): got %v, want %v", c.name, c.x, c.y, got, c.want)
		}
	}
}

func TestClassifyRegionMinimumPaneSize(t *testing.T) {
	// At exactly 2*bandPx in both dims, the entire interior is resize
	// zones — there's no inner area for swap or split.
	r := Rect{X: 0, Y: 0, W: 20, H: 20}
	band := 10.0
	// All these points have minD < band except the dead center where
	// minD == 10 (equal to band), which falls through to center.
	for _, c := range []struct {
		x, y float64
		want Region
	}{
		{0, 0, RegionResizeTop},   // tiebreak top wins over left at (0,0)
		{19, 0, RegionResizeTop},  // tiebreak top wins over right at (19,0)
		{0, 19, RegionResizeLeft}, // (0,19): left=0 wins over bottom=1
		{5, 5, RegionResizeTop},
		{15, 5, RegionResizeTop},
		{5, 15, RegionResizeBottom},  // (5,15): bottom=5 ties with left=5, bottom wins (checked first)
		{15, 15, RegionResizeBottom}, // (15,15): bottom=5 ties with right=5, bottom wins
	} {
		if got := ClassifyRegion(r, band, c.x, c.y); got != c.want {
			t.Errorf("@(%v,%v): got %v, want %v", c.x, c.y, got, c.want)
		}
	}
}

func TestClassifyRegionRazorThin(t *testing.T) {
	// A 20×100 pane has full-width resize-top + resize-bottom. The
	// strip y∈[10,10] is degenerate (zero-width middle). Anywhere
	// else → resize.
	r := Rect{X: 0, Y: 0, W: 100, H: 20}
	band := 10.0
	for _, c := range []struct {
		x, y float64
		want Region
	}{
		{50, 1, RegionResizeTop},
		{50, 9, RegionResizeTop},
		{50, 11, RegionResizeBottom},
		{50, 19, RegionResizeBottom},
		{1, 5, RegionResizeLeft},
		{99, 5, RegionResizeRight},
	} {
		if got := ClassifyRegion(r, band, c.x, c.y); got != c.want {
			t.Errorf("@(%v,%v): got %v, want %v", c.x, c.y, got, c.want)
		}
	}
}

func TestClassifyRegionDegradesAt30(t *testing.T) {
	// At 30×30 the inner area is 10×10 — fully inside the swap zone
	// (1/3 of 30 = 10..20). No split zone exists at this size.
	r := Rect{X: 0, Y: 0, W: 30, H: 30}
	band := 10.0
	for _, c := range []struct {
		x, y float64
		want Region
	}{
		{15, 15, RegionSwap},
		{12, 12, RegionSwap},
		{18, 18, RegionSwap},
		{5, 15, RegionResizeLeft},
		{15, 5, RegionResizeTop},
	} {
		if got := ClassifyRegion(r, band, c.x, c.y); got != c.want {
			t.Errorf("@(%v,%v): got %v, want %v", c.x, c.y, got, c.want)
		}
	}
}

func TestClassifyRegionWithOffset(t *testing.T) {
	// Pane not at origin; classifier should respect r.X / r.Y.
	r := Rect{X: 100, Y: 200, W: 60, H: 60}
	band := 10.0
	if got := ClassifyRegion(r, band, 130, 230); got != RegionSwap {
		t.Errorf("center: got %v", got)
	}
	if got := ClassifyRegion(r, band, 105, 230); got != RegionResizeLeft {
		t.Errorf("offset left edge: got %v", got)
	}
	if got := ClassifyRegion(r, band, 130, 218); got != RegionSplitTop {
		t.Errorf("split top: got %v", got)
	}
}

func TestClassifyRegionEmptyRect(t *testing.T) {
	if got := ClassifyRegion(Rect{}, 10, 0, 0); got != RegionNone {
		t.Errorf("empty rect: got %v", got)
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

func TestSplitGestureActive(t *testing.T) {
	cases := []struct {
		name                       string
		side                       Side
		startX, startY, curX, curY float64
		want                       bool
	}{
		{"top, dragging down -> active", SideTop, 100, 100, 100, 120, true},
		{"top, dragging up -> not active", SideTop, 100, 100, 100, 80, false},
		{"bottom, dragging up -> active", SideBottom, 100, 100, 100, 80, true},
		{"bottom, dragging down -> not active", SideBottom, 100, 100, 100, 120, false},
		{"left, dragging right -> active", SideLeft, 100, 100, 120, 100, true},
		{"left, dragging left -> not active", SideLeft, 100, 100, 80, 100, false},
		{"right, dragging left -> active", SideRight, 100, 100, 80, 100, true},
		{"right, dragging right -> not active", SideRight, 100, 100, 120, 100, false},
		{"top, no movement -> not active", SideTop, 100, 100, 100, 100, false},
	}
	for _, c := range cases {
		got := SplitGestureActive(c.side, c.startX, c.startY, c.curX, c.curY)
		if got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSplitClampedPosition(t *testing.T) {
	// 1000x800 pane at origin, band 10 -> valid Y range [20, 780], valid X range [20, 980].
	pr := Rect{X: 0, Y: 0, W: 1000, H: 800}
	band := 10.0

	pos, ok := SplitClampedPosition(SideTop, pr, band, 500, 400)
	if !ok || pos != 400 {
		t.Errorf("top middle: got (%v,%v), want (400,true)", pos, ok)
	}
	pos, ok = SplitClampedPosition(SideTop, pr, band, 500, 5)
	if ok || pos != 20 {
		t.Errorf("top above valid: got (%v,%v), want (20,false)", pos, ok)
	}
	pos, ok = SplitClampedPosition(SideTop, pr, band, 500, 900)
	if ok || pos != 780 {
		t.Errorf("top below valid: got (%v,%v), want (780,false)", pos, ok)
	}
	pos, ok = SplitClampedPosition(SideLeft, pr, band, 5, 400)
	if ok || pos != 20 {
		t.Errorf("left of valid: got (%v,%v), want (20,false)", pos, ok)
	}
	pos, ok = SplitClampedPosition(SideRight, pr, band, 990, 400)
	if ok || pos != 980 {
		t.Errorf("right of valid: got (%v,%v), want (980,false)", pos, ok)
	}
	// Pane too small to split.
	tiny := Rect{X: 0, Y: 0, W: 30, H: 30}
	_, ok = SplitClampedPosition(SideTop, tiny, band, 15, 15)
	if ok {
		t.Error("tiny pane: should not be in valid range")
	}
}

func TestSplitRect(t *testing.T) {
	c := Rect{X: 10, Y: 20, W: 100, H: 80}
	// Horizontal: A = top, B = bottom; ratio 0.25 -> A.H = 20.
	a, b := SplitRect(c, Horizontal, 0.25)
	nearRect(t, "h A", a, Rect{X: 10, Y: 20, W: 100, H: 20})
	nearRect(t, "h B", b, Rect{X: 10, Y: 40, W: 100, H: 60})
	// Vertical: A = left, B = right; ratio 0.25 -> A.W = 25.
	a, b = SplitRect(c, Vertical, 0.25)
	nearRect(t, "v A", a, Rect{X: 10, Y: 20, W: 25, H: 80})
	nearRect(t, "v B", b, Rect{X: 35, Y: 20, W: 75, H: 80})
	// Out-of-range ratios clamp to [0,1]: ratio>1 gives A the whole rect.
	a, b = SplitRect(c, Horizontal, 1.5)
	nearRect(t, "clamp-hi A", a, Rect{X: 10, Y: 20, W: 100, H: 80})
	nearRect(t, "clamp-hi B", b, Rect{X: 10, Y: 100, W: 100, H: 0})
	a, _ = SplitRect(c, Vertical, -0.5)
	nearRect(t, "clamp-lo A", a, Rect{X: 10, Y: 20, W: 0, H: 80})
}

func TestSplitRatioFromPos(t *testing.T) {
	// SplitRatioFromPos inverts a clamped position into a new-pane fraction.
	pr := Rect{X: 100, Y: 200, W: 400, H: 800}
	cases := []struct {
		side Side
		pos  float64
		want float64
	}{
		{SideTop, 400, 0.25},    // (400-200)/800
		{SideBottom, 400, 0.75}, // (1000-400)/800
		{SideLeft, 200, 0.25},   // (200-100)/400
		{SideRight, 200, 0.75},  // (500-200)/400
	}
	for _, c := range cases {
		if got := SplitRatioFromPos(c.side, pr, c.pos); absf(got-c.want) > 1e-9 {
			t.Errorf("side %v pos %v: got %v, want %v", c.side, c.pos, got, c.want)
		}
	}
}

func TestClampRatioToMinPx(t *testing.T) {
	// Horizontal split clamps on height; vertical on width. minPx 80 over a
	// 400-extent => minR = 0.2, so the valid band is [0.2, 0.8].
	hc := Rect{W: 1000, H: 400}
	if got := ClampRatioToMinPx(hc, Horizontal, 0.05, 80); absf(got-0.2) > 1e-9 {
		t.Errorf("clamp low: got %v, want 0.2", got)
	}
	if got := ClampRatioToMinPx(hc, Horizontal, 0.95, 80); absf(got-0.8) > 1e-9 {
		t.Errorf("clamp high: got %v, want 0.8", got)
	}
	if got := ClampRatioToMinPx(hc, Horizontal, 0.5, 80); absf(got-0.5) > 1e-9 {
		t.Errorf("in-range unchanged: got %v, want 0.5", got)
	}
	// Vertical uses width; same 1000 extent -> minR = 0.08.
	vc := Rect{W: 1000, H: 400}
	if got := ClampRatioToMinPx(vc, Vertical, 0.01, 80); absf(got-0.08) > 1e-9 {
		t.Errorf("vertical clamp low: got %v, want 0.08", got)
	}
	// Degenerate extent returns the ratio unchanged.
	if got := ClampRatioToMinPx(Rect{W: 0, H: 0}, Vertical, 0.3, 80); got != 0.3 {
		t.Errorf("degenerate: got %v, want 0.3", got)
	}
	// minR capped at 0.5 when minPx exceeds half the extent (range collapses
	// to the midpoint rather than inverting).
	if got := ClampRatioToMinPx(Rect{W: 100, H: 100}, Vertical, 0.1, 90); absf(got-0.5) > 1e-9 {
		t.Errorf("minR cap: got %v, want 0.5", got)
	}
}

func TestRectContains(t *testing.T) {
	r := Rect{X: 10, Y: 20, W: 30, H: 40}
	cases := []struct {
		name   string
		x, y   float64
		want   bool
	}{
		{"inside", 25, 30, true},
		{"top-left corner — inclusive", 10, 20, true},
		{"bottom-right corner — exclusive", 40, 60, false},
		{"left edge — inclusive", 10, 30, true},
		{"right edge — exclusive", 40, 30, false},
		{"above", 25, 19, false},
		{"left of", 9, 30, false},
	}
	for _, c := range cases {
		if got := r.Contains(c.x, c.y); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRegionCategoriesAndSide(t *testing.T) {
	type want struct {
		isResize, isSplit, isSwap bool
		side                      Side
	}
	cases := map[Region]want{
		RegionNone:         {false, false, false, SideTop},
		RegionSwap:         {false, false, true, SideTop},
		RegionResizeTop:    {true, false, false, SideTop},
		RegionResizeBottom: {true, false, false, SideBottom},
		RegionResizeLeft:   {true, false, false, SideLeft},
		RegionResizeRight:  {true, false, false, SideRight},
		RegionSplitTop:     {false, true, false, SideTop},
		RegionSplitBottom:  {false, true, false, SideBottom},
		RegionSplitLeft:    {false, true, false, SideLeft},
		RegionSplitRight:   {false, true, false, SideRight},
	}
	for r, w := range cases {
		if got := r.IsResize(); got != w.isResize {
			t.Errorf("%v.IsResize = %v, want %v", r, got, w.isResize)
		}
		if got := r.IsSplit(); got != w.isSplit {
			t.Errorf("%v.IsSplit = %v, want %v", r, got, w.isSplit)
		}
		if got := r.IsSwap(); got != w.isSwap {
			t.Errorf("%v.IsSwap = %v, want %v", r, got, w.isSwap)
		}
		if got := r.Side(); got != w.side {
			t.Errorf("%v.Side = %v, want %v", r, got, w.side)
		}
	}
}
