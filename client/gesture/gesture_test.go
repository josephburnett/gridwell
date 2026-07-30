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
				OverEmbed:  true,
				InGridView: true, OverTile: true, InTileCenter: true,
				Region: swap,
			},
			want: EmbedHint,
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
	// A corridor spanning [0, 600] as pane.CorridorSpan reports it. Closing
	// is the corridor-EDGE gesture (issue #204): only the CloseBandPx at the
	// span's own edges closes — the minimum walls (one pane-minimum in, where
	// a legal drag clamps) are a resize clamp, never a close threshold.
	const start, end = 0.0, 600.0

	tests := []struct {
		name         string
		dir          pane.Direction
		sx, sy       float64
		wantCollapse Collapse
	}{
		{"vertical mid — no collapse", pane.Vertical, 300, 100, CollapseNone},
		{"vertical at the minimum wall — resize, not close (#204)", pane.Vertical, 32, 100, CollapseNone},
		{"vertical past the wall, short of the edge — still resize (#204)", pane.Vertical, 20, 100, CollapseNone},
		{"vertical in the edge band — A closes", pane.Vertical, 4, 100, CollapseA},
		{"vertical at the band boundary — A closes (inclusive)", pane.Vertical, start + CloseBandPx, 100, CollapseA},
		{"vertical in the far band — B closes", pane.Vertical, 596, 100, CollapseB},
		{"vertical just inside the far band boundary — no collapse", pane.Vertical, end - CloseBandPx - 1, 100, CollapseNone},
		{"horizontal mid — no collapse", pane.Horizontal, 100, 300, CollapseNone},
		{"horizontal in the edge band — A closes", pane.Horizontal, 100, 4, CollapseA},
		{"horizontal in the far band — B closes", pane.Horizontal, 100, 596, CollapseB},
		// A bare click (issue #204): the verdict cursor is the arm point,
		// which sits within the grab band of a divider — and a divider is
		// always at least a pane-minimum from the corridor edge, so a click
		// can never land in a close band.
		{"cursor at a divider near the wall — never a close", pane.Vertical, 34, 100, CollapseNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResizeOutcome(tt.dir, tt.sx, tt.sy, start, end); got != tt.wantCollapse {
				t.Errorf("collapse = %v, want %v", got, tt.wantCollapse)
			}
		})
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
