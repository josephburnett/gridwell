package wsbar

import "testing"

// Render and input read the same rects, so the crumb drawn at a point is
// the crumb a click there resolves to.
func TestLayoutAndHitTestAgree(t *testing.T) {
	segs := Layout(squares(7), 900)
	if len(segs) != 7 {
		t.Fatalf("segments = %d, want all 7 (room for them)", len(segs))
	}
	for _, s := range segs {
		got, ok := At(segs, s.X+s.W/2)
		if !ok || got.Index != s.Index {
			t.Errorf("center of segment %+v hit-tests as %+v (ok=%v)", s, got, ok)
		}
	}
	last := segs[len(segs)-1]
	if _, ok := At(segs, last.X+last.W+50); ok {
		t.Error("beyond the last crumb must hit nothing")
	}
	if _, ok := At(nil, 10); ok {
		t.Error("empty bar must hit nothing")
	}
}

// Every crumb is a full RowH square, abutting its neighbor, starting at
// x=0 — one uniform chain.
func TestLayoutSquares(t *testing.T) {
	segs := Layout(squares(3), 1000)
	if segs[0].X != 0 || segs[0].Index != 0 {
		t.Fatalf("segment 0 = %+v, want index 0 at x=0", segs[0])
	}
	for i, s := range segs {
		if s.W != RowH {
			t.Fatalf("crumb w = %v, want the full square %v (no shrinking)", s.W, RowH)
		}
		if i > 0 && s.X != segs[i-1].X+segs[i-1].W {
			t.Fatalf("segment %d does not abut its neighbor", i)
		}
	}
}

// Overflow truncates from the left: the tail — where you are — keeps
// priority, survivors keep full size, and Index keeps addressing the
// caller's full crumb list.
func TestLayoutTruncatesFromLeft(t *testing.T) {
	width := SlotW + 3*RowH + 10 // room for exactly 3 squares
	segs := Layout(squares(10), width)
	if len(segs) != 3 {
		t.Fatalf("visible = %d, want 3", len(segs))
	}
	if segs[0].Index != 7 || segs[2].Index != 9 {
		t.Fatalf("visible indexes = [%d..%d], want the tail [7..9]", segs[0].Index, segs[2].Index)
	}
	if segs[0].X != 0 {
		t.Fatalf("first visible crumb starts at %v, want 0", segs[0].X)
	}
	// A truncated-away crumb has no rect; SegmentAt says so.
	if _, ok := SegmentAt(segs, 2); ok {
		t.Error("truncated crumb must not resolve")
	}
	if s, ok := SegmentAt(segs, 9); !ok || s.Index != 9 {
		t.Errorf("tail crumb must resolve: %+v ok=%v", s, ok)
	}
}

func TestLayoutDegenerate(t *testing.T) {
	if segs := Layout(nil, 900); segs != nil {
		t.Errorf("no crumbs: %v, want nil", segs)
	}
	if segs := Layout(squares(5), SlotW+10); segs != nil {
		t.Errorf("no room for even one square: %v, want nil", segs)
	}
}

// squares is the all-chain-crumb width list.
func squares(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = RowH
	}
	return out
}

// A boundary crumb is a wide bar among the squares; truncation still drops
// whole crumbs from the left, wide or not.
func TestLayoutMixedWidths(t *testing.T) {
	widths := []float64{RowH, BoundaryW, RowH, RowH}
	segs := Layout(widths, 900)
	if len(segs) != 4 || segs[1].W != BoundaryW || segs[2].X != RowH+BoundaryW {
		t.Fatalf("mixed layout = %+v", segs)
	}
	// Width for the last three only (boundary + 2 squares): the leading
	// square drops.
	tight := SlotW + BoundaryW + 2*RowH + 4
	segs = Layout(widths, tight)
	if len(segs) != 3 || segs[0].Index != 1 || segs[0].W != BoundaryW {
		t.Fatalf("tight layout = %+v, want the tail starting at the boundary", segs)
	}
}

// The band is reserved layout: panes end exactly where it starts, whether or
// not a notice strip is up, and the two numbers come from one computation.
func TestBandReservesTheStripBelowThePanes(t *testing.T) {
	paneH, ok := Band(800, 0)
	if !ok || paneH != 800-RowH {
		t.Fatalf("Band(800, 0) = %v, %v; want %v, true", paneH, ok, 800-RowH)
	}
	// A notice strip takes its rows from below the band.
	paneH, ok = Band(800, 24)
	if !ok || paneH != 800-24-RowH {
		t.Fatalf("Band(800, 24) = %v, %v; want %v, true", paneH, ok, 800-24-RowH)
	}
}

// The bar rides the focused pane's span, but the band it rides in does not:
// the vertical reservation is the same number whatever has focus, so panes
// never resize as focus moves — only the chrome slides.
func TestRectRidesTheFocusedPane(t *testing.T) {
	const winW, winH = 1600.0, 800.0
	left, top, w, ok := Rect(winW, winH, 0, 0, 600)
	if !ok || left != 0 || w != 600 {
		t.Fatalf("left pane: x=%v w=%v ok=%v; want 0, 600, true", left, w, ok)
	}
	rightX, rightTop, rightW, ok := Rect(winW, winH, 0, 600, 1000)
	if !ok || rightX != 600 || rightW != 1000 {
		t.Fatalf("right pane: x=%v w=%v ok=%v; want 600, 1000, true", rightX, rightW, ok)
	}
	if rightTop != top {
		t.Fatalf("the band moved with focus: %v then %v", top, rightTop)
	}
	paneH, _ := Band(winH, 0)
	if top != paneH {
		t.Fatalf("bar top = %v, want the band's top edge %v", top, paneH)
	}
}

// A pane that cannot fit, or that hangs off an edge, still gets a bar wholly
// inside the window: the chrome is never drawn half off-screen.
func TestRectClampsIntoTheWindow(t *testing.T) {
	// Wider than the window: the window's width, centered on the pane.
	x, _, w, ok := Rect(1000, 800, 0, -100, 1200)
	if !ok || x != 0 || w != 1000 {
		t.Fatalf("oversize pane: x=%v w=%v ok=%v; want 0, 1000, true", x, w, ok)
	}
	// Hanging off the right edge: slid back in, full width kept.
	x, _, w, ok = Rect(1000, 800, 0, 800, 400)
	if !ok || x != 600 || w != 400 {
		t.Fatalf("overhanging pane: x=%v w=%v; want 600, 400", x, w)
	}
	// Hanging off the left edge: slid back in.
	x, _, w, ok = Rect(1000, 800, 0, -50, 400)
	if !ok || x != 0 || w != 400 {
		t.Fatalf("left-overhanging pane: x=%v w=%v; want 0, 400", x, w)
	}
}

// No pane to sit under, or no room for the band: no bar.
func TestRectRefusesWithNothingToSitUnder(t *testing.T) {
	if _, _, _, ok := Rect(1000, 800, 0, 0, 0); ok {
		t.Error("no focused pane must yield no bar")
	}
	if _, _, _, ok := Rect(0, 800, 0, 0, 400); ok {
		t.Error("no window width must yield no bar")
	}
	if _, _, _, ok := Rect(1000, RowH-1, 0, 0, 400); ok {
		t.Error("a window too short for the band must yield no bar")
	}
}

// The band's leftover space is nobody's: it is not the bar, and it is not a
// pane either, so a point there is swallowed rather than passed down.
func TestWhereSeparatesTheBarFromItsBand(t *testing.T) {
	const top = 768.0
	x, w := 600.0, 400.0
	cases := []struct {
		name   string
		px, py float64
		want   Zone
	}{
		{"above the band", x + 10, top - 1, ZoneOutside},
		{"below the band", x + 10, top + RowH, ZoneOutside},
		{"on the chrome", x + w/2, top + RowH/2, ZoneBar},
		{"the left edge is the bar's", x, top, ZoneBar},
		{"the right edge is not", x + w, top, ZoneBand},
		{"left of the bar", x - 1, top + 5, ZoneBand},
		{"far right of the bar", x + w + 300, top + 5, ZoneBand},
	}
	for _, c := range cases {
		if got := Where(c.px, c.py, x, top, w); got != c.want {
			t.Errorf("%s: Where(%v, %v) = %v, want %v", c.name, c.px, c.py, got, c.want)
		}
	}
}

func TestBandRefusesAWindowTooShortToHoldIt(t *testing.T) {
	// Not even one row left: no band, and the panes get what remains.
	paneH, ok := Band(RowH-1, 0)
	if ok || paneH != RowH-1 {
		t.Fatalf("Band(%v, 0) = %v, %v; want %v, false", RowH-1, paneH, ok, RowH-1)
	}
	// A strip taller than the window never yields a negative pane height.
	if paneH, ok := Band(10, 40); ok || paneH != 0 {
		t.Fatalf("Band(10, 40) = %v, %v; want 0, false", paneH, ok)
	}
}
