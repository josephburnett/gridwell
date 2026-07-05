package panebox

import (
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
	// If 2*border exceeds the side, W or H goes negative without
	// clamping. Verify we floor at zero in both dimensions.
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
		// Inside content (border = 10, so content is 10..90).
		{50, 50, true},
		{10, 10, true},  // inclusive of top-left of content
		{89, 89, true},  // inclusive of just-before bottom-right (half-open)
		{90, 50, false}, // exactly on the right edge of content
		// Outside content, inside pane.
		{5, 50, false},
		{50, 5, false},
		// Way outside.
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
	// scale > 1 multiplies the font.
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
	// Inner is 188×188 after sideInset=6. cellPx=64 → fit ratio
	// (1×1 file should be Fit(1,1,188,188,64) which equals min(188,188)/64.
	z := OvertakeZoom(r, 1, 1, 6, 64)
	if z <= 0 {
		t.Errorf("OvertakeZoom = %v, want > 0", z)
	}
}

func TestOvertakeZoomDegenerate(t *testing.T) {
	// Inner box collapses to zero: returns 1 (caller should still
	// be able to render at the natural scale rather than div-by-zero).
	r := pane.Rect{X: 0, Y: 0, W: 5, H: 5}
	z := OvertakeZoom(r, 1, 1, 6, 64)
	if z != 1 {
		t.Errorf("OvertakeZoom on degenerate pane = %v, want 1", z)
	}
}

// TestLiveViewInsetPinned pins the intended grab-gutter width. A
// WebContentsView eats all mouse input over its bounds, so the only pixels
// a user can click to grab a divider between two adjacent live panes are
// the 2×LiveViewInsetPx canvas strip between them. This test catches any
// accidental reduction below the comfortable-grab threshold (~10px total).
func TestLiveViewInsetPinned(t *testing.T) {
	const wantInset = 5.0
	if LiveViewInsetPx != wantInset {
		t.Errorf("LiveViewInsetPx = %v, want %v; reducing this makes the "+
			"pane divider too narrow to grab over adjacent live URL/shell tiles",
			LiveViewInsetPx, wantInset)
	}
}

// TestLiveViewGapBetweenAdjacentPanes verifies that two horizontally adjacent
// panes whose content boxes are computed with LiveViewInsetPx leave a gap of
// exactly 2×LiveViewInsetPx between them — the grabbable canvas strip.
// This is the seam test: it crosses the layout→contentbox boundary that was
// previously untested ("no test pins the live-view gap").
func TestLiveViewGapBetweenAdjacentPanes(t *testing.T) {
	// Two 200×300 panes side by side, touching at x=200.
	left := pane.Rect{X: 0, Y: 0, W: 200, H: 300}
	right := pane.Rect{X: 200, Y: 0, W: 200, H: 300}

	lb := ContentBox(left, LiveViewInsetPx)
	rb := ContentBox(right, LiveViewInsetPx)

	// The right edge of left's content box and the left edge of right's
	// content box must be exactly 2×LiveViewInsetPx apart.
	gap := rb.X - (lb.X + lb.W)
	want := 2 * LiveViewInsetPx
	if gap != want {
		t.Errorf("gap between adjacent content boxes = %v, want %v (2×LiveViewInsetPx)",
			gap, want)
	}
}

// TestLiveViewContentBoxDegeneratePane verifies that a pane too narrow for
// the inset returns a zero-size content box rather than a negative one.
// A degenerate view must be hidden by the caller; this ensures ContentBox
// never returns negative dimensions that would produce an invalid native view.
func TestLiveViewContentBoxDegeneratePane(t *testing.T) {
	// Pane narrower than 2×LiveViewInsetPx on both axes.
	r := pane.Rect{X: 10, Y: 10, W: 3, H: 3}
	b := ContentBox(r, LiveViewInsetPx)
	if b.W != 0 || b.H != 0 {
		t.Errorf("ContentBox on tiny pane = W:%v H:%v, want W:0 H:0", b.W, b.H)
	}
}
