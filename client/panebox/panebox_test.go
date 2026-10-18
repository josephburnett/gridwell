package panebox

import (
	"github.com/josephburnett/gridwell/client/preview"
	"math"
	"testing"

	"github.com/josephburnett/gridwell/client/pane"
)

func TestContentBoxInsetsByBorder(t *testing.T) {
	r := pane.Rect{X: 10, Y: 20, W: 100, H: 80}
	got := ContentBox(r, 6)
	want := pane.Rect{X: 16, Y: 26, W: 88, H: 68}
	if got != want {
		t.Errorf("ContentBox = %+v, want %+v", got, want)
	}
}

func TestContentBoxClampsToZero(t *testing.T) {
	// Without clamping, a border wider than half the side goes negative.
	r := pane.Rect{X: 0, Y: 0, W: 5, H: 4}
	got := ContentBox(r, 10)
	if got.W != 0 || got.H != 0 {
		t.Errorf("ContentBox W=%v H=%v, want 0, 0", got.W, got.H)
	}
}

func TestPointInContent(t *testing.T) {
	r := pane.Rect{X: 0, Y: 0, W: 100, H: 100}
	cases := []struct {
		sx, sy float64
		want   bool
	}{
		// With border 10 the content runs from 10 to 90, half-open.
		{50, 50, true},
		{10, 10, true},
		{89, 89, true},
		{90, 50, false},
		// Inside the pane but outside the content.
		{5, 50, false},
		{50, 5, false},
		// Outside the pane.
		{-1, 50, false},
		{200, 50, false},
	}
	for _, c := range cases {
		got := PointInContent(r, 10, c.sx, c.sy)
		if got != c.want {
			t.Errorf("PointInContent(%v, %v) = %v, want %v", c.sx, c.sy, got, c.want)
		}
	}
}

func TestTextareaBox(t *testing.T) {
	r := pane.Rect{X: 100, Y: 200, W: 400, H: 300}
	got, fontPx := TextareaBox(r, 6, 14, 1)
	want := pane.Rect{X: 106, Y: 206, W: 388, H: 288}
	if got != want {
		t.Errorf("TextareaBox = %+v, want %+v", got, want)
	}
	if fontPx != 14 {
		t.Errorf("fontPx = %v, want 14", fontPx)
	}
	// A scale above 1 multiplies the font.
	_, fp := TextareaBox(r, 6, 14, 1.5)
	if math.Abs(fp-21) > 1e-9 {
		t.Errorf("scaled fontPx = %v, want 21", fp)
	}
}

func TestInnerBoxMatchesTextareaBox(t *testing.T) {
	r := pane.Rect{X: 50, Y: 60, W: 200, H: 150}
	inner := InnerBox(r, 6)
	tb, _ := TextareaBox(r, 6, 14, 1)
	if inner != tb {
		t.Errorf("InnerBox = %+v, TextareaBox = %+v; must agree", inner, tb)
	}
}

func TestPointInInner(t *testing.T) {
	r := pane.Rect{X: 0, Y: 0, W: 100, H: 100}
	if !PointInInner(r, 6, 50, 50) {
		t.Error("center of pane should be inside inner")
	}
	if PointInInner(r, 6, 5, 50) {
		t.Error("inside border should be outside inner")
	}
}

func TestOvertakeZoom(t *testing.T) {
	r := pane.Rect{X: 0, Y: 0, W: 200, H: 200}
	z := FitZoom(r, 1, 1, 6, 64)
	if z <= 0 {
		t.Errorf("FitZoom = %v, want > 0", z)
	}
}

func TestOvertakeZoomDegenerate(t *testing.T) {
	// A collapsed inner box returns 1 so the caller renders at the natural
	// scale instead of dividing by zero.
	r := pane.Rect{X: 0, Y: 0, W: 5, H: 5}
	z := FitZoom(r, 1, 1, 6, 64)
	if z != 1 {
		t.Errorf("FitZoom on degenerate pane = %v, want 1", z)
	}
}

// TestLiveViewInsetPinned pins the grab-gutter width. A WebContentsView eats
// all mouse input over its bounds, so the 2×LiveViewInsetPx canvas gap
// between two adjacent live panes is the only place a divider can be grabbed,
// and below roughly 10px total it is hard to hit.
func TestLiveViewInsetPinned(t *testing.T) {
	const wantInset = 5.0
	if LiveViewInsetPx != wantInset {
		t.Errorf("LiveViewInsetPx = %v, want %v; reducing this makes the "+
			"pane divider too narrow to grab over adjacent live URL/shell tiles",
			LiveViewInsetPx, wantInset)
	}
}

// TestLiveViewGapBetweenAdjacentPanes crosses the layout-to-contentbox seam:
// two adjacent panes inset by LiveViewInsetPx leave exactly 2×LiveViewInsetPx
// of grabbable canvas between them.
func TestLiveViewGapBetweenAdjacentPanes(t *testing.T) {
	// Two 200×300 panes touching at x=200.
	left := pane.Rect{X: 0, Y: 0, W: 200, H: 300}
	right := pane.Rect{X: 200, Y: 0, W: 200, H: 300}

	lb := ContentBox(left, LiveViewInsetPx)
	rb := ContentBox(right, LiveViewInsetPx)

	gap := rb.X - (lb.X + lb.W)
	want := 2 * LiveViewInsetPx
	if gap != want {
		t.Errorf("gap between adjacent content boxes = %v, want %v (2×LiveViewInsetPx)",
			gap, want)
	}
}

// TestLiveViewContentBoxDegeneratePane pins a pane too narrow for the inset to
// a zero-size content box. Negative dimensions would make an invalid native
// view; the caller hides a degenerate one.
func TestLiveViewContentBoxDegeneratePane(t *testing.T) {
	r := pane.Rect{X: 10, Y: 10, W: 3, H: 3}
	b := ContentBox(r, LiveViewInsetPx)
	if b.W != 0 || b.H != 0 {
		t.Errorf("ContentBox on tiny pane = W:%v H:%v, want W:0 H:0", b.W, b.H)
	}
}

// A modal centers on the pane you acted in.
func TestModalCardPos_CentersOnThePane(t *testing.T) {
	// A 400×200 card on the right half of a 1000×800 window.
	r := pane.Rect{X: 500, Y: 0, W: 500, H: 800}
	x, y := ModalCardPos(r, 400, 200, 1000, 800)
	if x != 550 || y != 300 {
		t.Errorf("pos = (%v,%v), want (550,300) — the pane's center", x, y)
	}
}

func TestModalCardPos_ClampsToTheWindow(t *testing.T) {
	// Centering on a narrow pane at the right edge would push the card past the
	// window, so it clamps flush.
	r := pane.Rect{X: 900, Y: 700, W: 100, H: 100}
	x, y := ModalCardPos(r, 400, 200, 1000, 800)
	if x != 600 || y != 600 {
		t.Errorf("pos = (%v,%v), want (600,600) — flush against the window edge", x, y)
	}
	// A card wider than the window pins to 0 so its first field stays
	// reachable.
	x, y = ModalCardPos(r, 1200, 900, 1000, 800)
	if x != 0 || y != 0 {
		t.Errorf("oversized card pos = (%v,%v), want (0,0)", x, y)
	}
}

// The parked frame and the live view share the pane's content box, with
// nothing carved out of it, because the one bar lives below every pane. A
// capture taken at the live bounds and contain-fit into the fallback box lands
// where the view was, with no letterbox and no shift.
func TestContentBoxIsTheFallbackBox(t *testing.T) {
	r := pane.Rect{X: 100, Y: 40, W: 600, H: 400}
	live := ContentBox(r, 2)
	dx, dy, dw, dh, ok := preview.ContainDstRect(live.W, live.H, live.X, live.Y, live.W, live.H)
	if !ok || dx != live.X || dy != live.Y || dw != live.W || dh != live.H {
		t.Fatalf("a capture drawn into its own box moved: (%v,%v,%v,%v)", dx, dy, dw, dh)
	}
	// The box runs to the pane's bottom border, because a live view fills the
	// pane and no band is reserved out of it.
	if !PointInContent(r, 2, 300, 300) || !PointInContent(r, 2, 300, 437) {
		t.Error("hit-test: the content box reaches the pane's bottom border")
	}
	if PointInContent(r, 2, 300, 441) {
		t.Error("hit-test: past the pane's bottom edge is outside")
	}
}

// A live view owns the pixels of its own content box only while it is painting
// there. A parked view owns nothing, and a frozen pane has no view to own
// anything.
//
// The armed-gesture row matters most. The release that ends a drag lands
// wherever the pointer is, often over a live url pane, and a handler that
// swallows it there leaves the drag armed forever, which also parks every live
// view for good because the park is keyed off the same armed drag.
func TestLiveViewOwnsPoint(t *testing.T) {
	r := pane.Rect{X: 100, Y: 40, W: 600, H: 400}
	const border = 2
	inX, inY := 400.0, 240.0
	outX, outY := 50.0, 240.0

	cases := []struct {
		name           string
		overlaysHidden bool
		hasLiveView    bool
		x, y           float64
		want           bool
	}{
		{"live view, point inside its content box", false, true, inX, inY, true},
		{"live view, point outside the pane", false, true, outX, outY, false},
		{"parked view (gesture armed / menu open / modal up)", true, true, inX, inY, false},
		{"frozen pane: no live view owns nothing", false, false, inX, inY, false},
		{"parked and frozen", true, false, inX, inY, false},
	}
	for _, c := range cases {
		got := LiveViewOwnsPoint(c.overlaysHidden, c.hasLiveView, r, border, c.x, c.y)
		if got != c.want {
			t.Errorf("%s: LiveViewOwnsPoint = %v, want %v", c.name, got, c.want)
		}
	}

	// The owned region is PointInContent's. One box and one hit-test keep the
	// parked capture the canvas draws and the live view it replaces from
	// disagreeing about which points are theirs.
	for _, p := range [][2]float64{{inX, inY}, {outX, outY}, {100, 40}, {700, 440}, {702, 240}} {
		if got, want := LiveViewOwnsPoint(false, true, r, border, p[0], p[1]), PointInContent(r, border, p[0], p[1]); got != want {
			t.Errorf("at (%v,%v): owns=%v but PointInContent=%v", p[0], p[1], got, want)
		}
	}
}
