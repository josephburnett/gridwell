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
	tr.Root.Split.Ratio = 0.25

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
	tr.Root.Split.Ratio = 0.4

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
	tr.SetFocus(b)
	cP, _ := tr.Split(Vertical)
	c := cP.ID

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
	_, _ = tr.Split(Horizontal)

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
	bP, _ := tr.Split(Horizontal)
	b := bP.ID
	tr.SetFocus(b)
	_, _ = tr.Split(Vertical)

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

func TestClassifyRegionLargePane(t *testing.T) {
	r := Rect{X: 0, Y: 0, W: 100, H: 100}
	cases := []struct {
		name string
		x, y float64
		want Region
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
	_, _ = tr.Split(Horizontal)
	got := Dividers(tr, Rect{W: 1000, H: 800}, 0) // 0 → default 4
	if got[0].Rect.H != 4 {
		t.Errorf("default thickness = %v, want 4", got[0].Rect.H)
	}
}

func TestSplitClampedPosition(t *testing.T) {
	// The clamp leaves MinPanePx (32) on each side — the same universal
	// minimum every other sizing path enforces. A 1000x800 pane at the
	// origin has valid Y range [32, 768] and valid X range [32, 968].
	pr := Rect{X: 0, Y: 0, W: 1000, H: 800}

	pos, ok := SplitClampedPosition(SideTop, pr, 500, 400)
	if !ok || pos != 400 {
		t.Errorf("top middle: got (%v,%v), want (400,true)", pos, ok)
	}
	pos, ok = SplitClampedPosition(SideTop, pr, 500, 5)
	if ok || pos != MinPanePx {
		t.Errorf("top above valid: got (%v,%v), want (%v,false)", pos, ok, MinPanePx)
	}
	pos, ok = SplitClampedPosition(SideTop, pr, 500, 900)
	if ok || pos != 800-MinPanePx {
		t.Errorf("top below valid: got (%v,%v), want (%v,false)", pos, ok, 800-MinPanePx)
	}
	pos, ok = SplitClampedPosition(SideLeft, pr, 5, 400)
	if ok || pos != MinPanePx {
		t.Errorf("left of valid: got (%v,%v), want (%v,false)", pos, ok, MinPanePx)
	}
	pos, ok = SplitClampedPosition(SideRight, pr, 990, 400)
	if ok || pos != 1000-MinPanePx {
		t.Errorf("right of valid: got (%v,%v), want (%v,false)", pos, ok, 1000-MinPanePx)
	}
	// Pane too small to hold two minimum panes: unsplittable.
	tiny := Rect{X: 0, Y: 0, W: 2*MinPanePx - 1, H: 2*MinPanePx - 1}
	_, ok = SplitClampedPosition(SideTop, tiny, 31, 31)
	if ok {
		t.Error("sub-2*min pane: should not be splittable")
	}
}

// TestMinPanePxValue pins the universal minimum: every sizing path —
// left-drag clamp, right-drag crush threshold, split clamp, the programmatic
// ephemeral split — reads this constant.
func TestMinPanePxValue(t *testing.T) {
	if MinPanePx != 32.0 {
		t.Errorf("MinPanePx = %v, want 32", MinPanePx)
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

func TestRectContains(t *testing.T) {
	r := Rect{X: 10, Y: 20, W: 30, H: 40}
	cases := []struct {
		name string
		x, y float64
		want bool
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

func TestDividerOnSide(t *testing.T) {
	pr := Rect{X: 0, Y: 0, W: 100, H: 100}
	// Vertical divider centered on the pane's right edge (midX = 100).
	vRight := Divider{Dir: Vertical, Rect: Rect{X: 99, Y: 0, W: 2, H: 100}}
	// Vertical divider on the left edge (midX = 0).
	vLeft := Divider{Dir: Vertical, Rect: Rect{X: -1, Y: 0, W: 2, H: 100}}
	// Horizontal divider on the top edge (midY = 0).
	hTop := Divider{Dir: Horizontal, Rect: Rect{X: 0, Y: -1, W: 100, H: 2}}
	// Horizontal divider on the bottom edge (midY = 100).
	hBottom := Divider{Dir: Horizontal, Rect: Rect{X: 0, Y: 99, W: 100, H: 2}}
	// A vertical divider far away (no match).
	vFar := Divider{Dir: Vertical, Rect: Rect{X: 499, Y: 0, W: 2, H: 100}}

	cases := []struct {
		name string
		divs []Divider
		side Side
		want int
	}{
		{"right edge match", []Divider{vFar, vRight}, SideRight, 1},
		{"left edge match", []Divider{vLeft}, SideLeft, 0},
		{"top edge match", []Divider{hTop}, SideTop, 0},
		{"bottom edge match", []Divider{hBottom}, SideBottom, 0},
		{"wrong direction for side (vertical vs Top)", []Divider{vRight}, SideTop, -1},
		{"horizontal divider not on left", []Divider{hTop}, SideLeft, -1},
		{"position mismatch", []Divider{vFar}, SideRight, -1},
		{"empty list", nil, SideRight, -1},
		{"picks the adjacent one, not the far one", []Divider{vFar, vLeft}, SideLeft, 1},
	}
	for _, c := range cases {
		if got := DividerOnSide(c.divs, pr, c.side); got != c.want {
			t.Errorf("%s: DividerOnSide = %d, want %d", c.name, got, c.want)
		}
	}
}

// A T-intersection: pane P is the bottom-right of a 200x200 root, with a
// vertical divider on its left edge (x=100) and a horizontal one on its top
// edge (y=100). The two belong to different tree nodes; the grab returns at
// most one per axis.
func grabFixture() (Rect, []Divider) {
	pr := Rect{X: 100, Y: 100, W: 100, H: 100}
	vLeft := Divider{Dir: Vertical, Rect: Rect{X: 99, Y: 0, W: 2, H: 200}}
	hTop := Divider{Dir: Horizontal, Rect: Rect{X: 100, Y: 99, W: 100, H: 2}}
	return pr, []Divider{vLeft, hTop}
}

func TestGrabDividersCornerGrabsBothAxes(t *testing.T) {
	pr, divs := grabFixture()
	const band = 10.0
	cases := []struct {
		name                 string
		sx, sy               float64
		wantHoriz, wantVert  bool
		wantHSide, wantVSide Side
	}{
		// Inside the band of both edges: the corner.
		{"dead on the corner", 100, 100, true, true, SideTop, SideLeft},
		{"inside both bands", 103, 105, true, true, SideTop, SideLeft},
		// One axis only: on a divider's length, away from the other.
		{"mid left edge", 103, 150, false, true, SideTop, SideLeft},
		{"mid top edge", 150, 105, true, false, SideTop, SideLeft},
		// Neither: pane interior.
		{"pane middle", 150, 150, false, false, SideTop, SideLeft},
		// Tolerance edges: strictly inside the band arms, exactly at it does not.
		{"just inside both", 109.9, 109.9, true, true, SideTop, SideLeft},
		{"exactly at the band on both", 110, 110, false, false, SideTop, SideLeft},
		{"at the band vertically, inside horizontally", 109, 110, false, true, SideTop, SideLeft},
		{"at the band horizontally, inside vertically", 110, 109, true, false, SideTop, SideLeft},
		// The far edges have no divider: the band is there, the divider is not.
		{"bottom-right corner has no dividers", 199, 199, false, false, SideTop, SideLeft},
	}
	for _, c := range cases {
		g := GrabDividers(divs, pr, band, c.sx, c.sy)
		if g.HasHoriz != c.wantHoriz || g.HasVert != c.wantVert {
			t.Errorf("%s: GrabDividers(%v,%v) horiz=%v vert=%v, want horiz=%v vert=%v",
				c.name, c.sx, c.sy, g.HasHoriz, g.HasVert, c.wantHoriz, c.wantVert)
			continue
		}
		if g.HasHoriz && (g.HorizSide != c.wantHSide || divs[g.Horiz].Dir != Horizontal) {
			t.Errorf("%s: horiz side %v idx %d", c.name, g.HorizSide, g.Horiz)
		}
		if g.HasVert && (g.VertSide != c.wantVSide || divs[g.Vert].Dir != Vertical) {
			t.Errorf("%s: vert side %v idx %d", c.name, g.VertSide, g.Vert)
		}
		if g.Any() != (c.wantHoriz || c.wantVert) || g.Both() != (c.wantHoriz && c.wantVert) {
			t.Errorf("%s: Any/Both disagree with the fields", c.name)
		}
	}
}

// The near edge wins a tie, so the tiebreak is top over bottom and left over
// right — the same order ClassifyRegion uses.
func TestGrabDividersPicksTheNearerEdgePerAxis(t *testing.T) {
	// A pane with a divider on every side; a press dead center of a pane
	// narrower than two bands must still resolve one side per axis.
	pr := Rect{X: 100, Y: 100, W: 16, H: 16}
	divs := []Divider{
		{Dir: Vertical, Rect: Rect{X: 99, Y: 0, W: 2, H: 400}},    // left, x=100
		{Dir: Vertical, Rect: Rect{X: 115, Y: 0, W: 2, H: 400}},   // right, x=116
		{Dir: Horizontal, Rect: Rect{X: 0, Y: 99, W: 400, H: 2}},  // top, y=100
		{Dir: Horizontal, Rect: Rect{X: 0, Y: 115, W: 400, H: 2}}, // bottom, y=116
	}
	const band = 10.0
	// Dead center: both distances are 8 on each axis, so the tie goes near.
	g := GrabDividers(divs, pr, band, 108, 108)
	if !g.Both() || g.HorizSide != SideTop || g.VertSide != SideLeft {
		t.Fatalf("centered tie: got %+v, want top/left", g)
	}
	// Nearer the far edges: the far side wins outright.
	g = GrabDividers(divs, pr, band, 114, 114)
	if !g.Both() || g.HorizSide != SideBottom || g.VertSide != SideRight {
		t.Fatalf("near the far edges: got %+v, want bottom/right", g)
	}
}

func TestGrabDividersEmptyRectAndNoDividers(t *testing.T) {
	pr, divs := grabFixture()
	if g := GrabDividers(divs, Rect{}, 10, 0, 0); g.Any() {
		t.Errorf("degenerate rect must grab nothing, got %+v", g)
	}
	if g := GrabDividers(nil, pr, 10, 100, 100); g.Any() {
		t.Errorf("no dividers must grab nothing, got %+v", g)
	}
	if g := (DividerGrab{}); g.Any() || g.Both() {
		t.Errorf("the zero value must grab nothing")
	}
}

// The corner grab arms one resize per axis, and one axis's crush can close a
// subtree containing the other's segment. HasSegment is what the release asks
// before flushing a second time.
func TestHasSegment(t *testing.T) {
	tr := NewTree()
	if _, err := tr.SplitOnSideAt(SideRight, 0.5); err != nil {
		t.Fatal(err)
	}
	left, right := tr.Root.Split.A, tr.Root.Split.B
	if !HasSegment(tr.Root, left) || !HasSegment(tr.Root, right) {
		t.Fatalf("both children must be present")
	}
	if !tr.RemoveSegment(right) {
		t.Fatalf("RemoveSegment(right) failed")
	}
	if HasSegment(tr.Root, right) {
		t.Errorf("a removed segment must not report present")
	}
	if !HasSegment(tr.Root, left) {
		t.Errorf("the hoisted survivor must still report present")
	}
}

func TestCanSplitAgreesWithTheClamp(t *testing.T) {
	for _, h := range []float64{2*MinPanePx - 1, 2 * MinPanePx, 2*MinPanePx + 1, 10 * MinPanePx} {
		r := Rect{W: 10 * MinPanePx, H: h}
		_, ok := SplitClampedPosition(SideBottom, r, r.W/2, r.H/2)
		if CanSplit(SideBottom, r) != ok {
			t.Errorf("H=%v: CanSplit=%v but the clamp says %v", h, CanSplit(SideBottom, r), ok)
		}
	}
	if CanSplit(SideRight, Rect{W: 2 * MinPanePx, H: 999}) || !CanSplit(SideRight, Rect{W: 2*MinPanePx + 1, H: 999}) {
		t.Error("the horizontal axis follows the same rule")
	}
}
