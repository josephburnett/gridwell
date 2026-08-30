package gesture

import (
	"testing"

	"github.com/josephburnett/gridwell/client/pane"
)

// resizeRegion / splitRegion / swapRegion are concrete Region values for
// the classifier table. ClassifyRegion is tested in the pane package; here
// we only need representatives of each predicate class.
func regionFor(t *testing.T, kind string) pane.Region {
	t.Helper()
	// A small pane with a generous band: the corners are resize, the edge
	// midpoints are split, the center is swap.
	r := pane.Rect{X: 0, Y: 0, W: 100, H: 100}
	band := 10.0
	switch kind {
	case "resize":
		return pane.ClassifyRegion(r, band, 2, 2) // top-left corner
	case "split":
		return pane.ClassifyRegion(r, band, 50, 2) // top edge midpoint
	case "swap":
		return pane.ClassifyRegion(r, band, 50, 50) // center
	}
	t.Fatalf("unknown region kind %q", kind)
	return 0
}

func TestClassifyPriority(t *testing.T) {
	resize := regionFor(t, "resize")
	split := regionFor(t, "split")
	swap := regionFor(t, "swap")

	tests := []struct {
		name string
		in   Input
		want Kind
	}{
		{
			name: "tile center is the clone handle",
			in: Input{
				InGridView: true, OverTile: true, InTileCenter: true,
				Region: swap,
			},
			want: TileCenter,
		},
		{
			name: "tile outside center is resize",
			in: Input{
				InGridView: true, OverTile: true, InTileCenter: false,
				Region: swap,
			},
			want: TileResize,
		},
		{
			name: "over tile but not in grid view (text/file descent) ignores tile",
			in: Input{
				InGridView: false, OverTile: true, InTileCenter: true,
				Region: swap,
			},
			want: Swap,
		},
		{
			// The right button always splits from a border, divider or
			// screen edge alike; resize and close are the left button.
			name: "resize region splits (divider resizing is the left button's)",
			in: Input{
				Region: resize,
			},
			want: Split,
		},
		{
			name: "split region",
			in:   Input{Region: split},
			want: Split,
		},
		{
			name: "swap region",
			in:   Input{Region: swap},
			want: Swap,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.in); got != tt.want {
				t.Errorf("Classify() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSplitOutcome(t *testing.T) {
	// A 200x200 pane; the side was already resolved by SplitSideFromDrag.
	r := pane.Rect{X: 0, Y: 0, W: 200, H: 200}
	ratio, ok := SplitOutcome(pane.SideTop, r, 100, 100)
	if !ok {
		t.Fatalf("SplitOutcome mid-pane = cancel, want ok")
	}
	// Cursor at y=100 in a 200-tall pane splitting off the top → ~0.5.
	if ratio < 0.4 || ratio > 0.6 {
		t.Errorf("ratio = %v, want ~0.5 for a mid-pane release", ratio)
	}
	// A release outside the min-pane clamp range cancels.
	if _, ok := SplitOutcome(pane.SideTop, r, 100, 500); ok {
		t.Errorf("far-outside release = ok, want cancel")
	}
}

// The side follows the drag: either side of a border behaves identically, the
// direction can flip mid-gesture, and a sub-threshold drag is inactive, so a
// bare right-click on a border never splits.
func TestSplitSideFromDrag(t *testing.T) {
	cases := []struct {
		name       string
		axis       pane.Direction
		dx, dy     float64
		want       pane.Side
		wantActive bool
	}{
		{"right drag opens on the left of the host", pane.Vertical, 40, 0, pane.SideLeft, true},
		{"left drag opens on the right of the host", pane.Vertical, -40, 0, pane.SideRight, true},
		{"down drag opens on the top of the host", pane.Horizontal, 0, 40, pane.SideTop, true},
		{"up drag opens on the bottom of the host", pane.Horizontal, 0, -40, pane.SideBottom, true},
		{"sub-threshold jitter is inactive", pane.Vertical, 4, 0, 0, false},
		{"bare click is inactive", pane.Vertical, 0, 0, 0, false},
	}
	for _, c := range cases {
		side, active := SplitSideFromDrag(c.axis, 100, 100, 100+c.dx, 100+c.dy)
		if active != c.wantActive || (active && side != c.want) {
			t.Errorf("%s: = (%v, %v), want (%v, %v)", c.name, side, active, c.want, c.wantActive)
		}
	}
	// Flip mid-gesture: same start, opposite cursor — opposite side.
	s1, _ := SplitSideFromDrag(pane.Vertical, 100, 0, 160, 0)
	s2, _ := SplitSideFromDrag(pane.Vertical, 100, 0, 40, 0)
	if s1 == s2 {
		t.Errorf("flip: both directions gave %v", s1)
	}
}

func TestResizeAffordance(t *testing.T) {
	cases := []struct {
		name       string
		region     pane.Region
		hasDivider bool
		wantArm    bool
		wantCursor string
	}{
		{"left band with divider -> ew", pane.RegionResizeLeft, true, true, "ew-resize"},
		{"right band with divider -> ew", pane.RegionResizeRight, true, true, "ew-resize"},
		{"top band with divider -> ns", pane.RegionResizeTop, true, true, "ns-resize"},
		{"bottom band with divider -> ns", pane.RegionResizeBottom, true, true, "ns-resize"},
		{"resize band but no divider on that side", pane.RegionResizeTop, false, false, ""},
		{"not a resize region", pane.RegionNone, true, false, ""},
	}
	for _, c := range cases {
		arm, cursor := ResizeAffordance(c.region, c.hasDivider)
		if arm != c.wantArm || cursor != c.wantCursor {
			t.Errorf("%s: ResizeAffordance(%v,%v) = (%v,%q), want (%v,%q)",
				c.name, c.region, c.hasDivider, arm, cursor, c.wantArm, c.wantCursor)
		}
	}
}
