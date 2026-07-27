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
			name: "embed beats everything",
			in: Input{
				OverEmbed:      true,
				OnCornerCircle: true, CanAscend: true,
				InGridView: true, OverTile: true, InTileCenter: true,
				Region: swap,
			},
			want: EmbedHint,
		},
		{
			name: "corner circle ascends when can-ascend",
			in: Input{
				OnCornerCircle: true, CanAscend: true,
				InGridView: true, OverTile: true,
				Region: swap,
			},
			want: Ascend,
		},
		{
			name: "corner circle at root (no ascend) does not arm ascend",
			in: Input{
				OnCornerCircle: true, CanAscend: false,
				Region: swap,
			},
			want: Swap,
		},
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
			// Issue #203: the right button ALWAYS splits from a border —
			// divider or screen edge alike; resize/close is the left button.
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
	// A 200x200 pane; split off the top.
	r := pane.Rect{X: 0, Y: 0, W: 200, H: 200}

	// No drag away from the top edge → cancel.
	if _, ok := SplitOutcome(pane.SideTop, r, 100, 5, 100, 5); ok {
		t.Errorf("SplitOutcome with no drag = ok, want cancel")
	}

	// Drag down well into the pane → active, ratio in (0,1).
	ratio, ok := SplitOutcome(pane.SideTop, r, 100, 5, 100, 100)
	if !ok {
		t.Fatalf("SplitOutcome with a real drag = cancel, want ok")
	}
	if ratio <= 0 || ratio >= 1 {
		t.Errorf("ratio = %v, want in (0,1)", ratio)
	}
	// Cursor at y=100 in a 200-tall pane splitting off the top → ~0.5.
	if ratio < 0.4 || ratio > 0.6 {
		t.Errorf("ratio = %v, want ~0.5 for a mid-pane release", ratio)
	}
}

func TestResizeOutcome(t *testing.T) {
	container := pane.Rect{X: 0, Y: 0, W: 200, H: 200}
	const closeThreshold = 20.0

	tests := []struct {
		name         string
		dir          pane.Direction
		sx, sy       float64
		wantCollapse Collapse
	}{
		{"vertical mid — no collapse", pane.Vertical, 100, 100, CollapseNone},
		{"vertical far left — A collapses", pane.Vertical, 5, 100, CollapseA},
		{"vertical far right — B collapses", pane.Vertical, 195, 100, CollapseB},
		{"horizontal mid — no collapse", pane.Horizontal, 100, 100, CollapseNone},
		{"horizontal far top — A collapses", pane.Horizontal, 100, 5, CollapseA},
		{"horizontal far bottom — B collapses", pane.Horizontal, 100, 195, CollapseB},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ratio, collapse := ResizeOutcome(container, tt.dir, tt.sx, tt.sy, closeThreshold)
			if collapse != tt.wantCollapse {
				t.Errorf("collapse = %v, want %v (ratio %v)", collapse, tt.wantCollapse, ratio)
			}
			if ratio < 0 || ratio > 1 {
				t.Errorf("ratio = %v, want in [0,1]", ratio)
			}
		})
	}
}

func TestResizeAffordance(t *testing.T) {
	cases := []struct {
		name       string
		inPlus     bool
		region     pane.Region
		hasDivider bool
		wantArm    bool
		wantCursor string
	}{
		{"left band with divider -> ew", false, pane.RegionResizeLeft, true, true, "ew-resize"},
		{"right band with divider -> ew", false, pane.RegionResizeRight, true, true, "ew-resize"},
		{"top band with divider -> ns", false, pane.RegionResizeTop, true, true, "ns-resize"},
		{"bottom band with divider -> ns", false, pane.RegionResizeBottom, true, true, "ns-resize"},
		{"corner circle wins over a resizable band", true, pane.RegionResizeLeft, true, false, ""},
		{"resize band but no divider on that side", false, pane.RegionResizeTop, false, false, ""},
		{"not a resize region", false, pane.RegionNone, true, false, ""},
		{"non-resize region ignores hasDivider", false, pane.RegionNone, true, false, ""},
	}
	for _, c := range cases {
		arm, cursor := ResizeAffordance(c.inPlus, c.region, c.hasDivider)
		if arm != c.wantArm || cursor != c.wantCursor {
			t.Errorf("%s: ResizeAffordance(%v,%v,%v) = (%v,%q), want (%v,%q)",
				c.name, c.inPlus, c.region, c.hasDivider, arm, cursor, c.wantArm, c.wantCursor)
		}
	}
}
