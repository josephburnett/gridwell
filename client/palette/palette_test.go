package palette

import (
	"math"
	"testing"
)

func makeLayout() Layout {
	return Layout{
		Cfg:      Default(),
		Pane:     Rect{X: 0, Y: 0, W: 1000, H: 800},
		PaneZoom: 1.0,
		NumTiles: 4,
	}
}

func TestPlusCenter(t *testing.T) {
	l := makeLayout()
	cx, cy := l.PlusCenter()
	if cx != 976 || cy != 776 {
		t.Errorf("PlusCenter = (%v,%v), want (976,776)", cx, cy)
	}
}

func TestPointInPlus(t *testing.T) {
	l := makeLayout()
	cx, cy := l.PlusCenter()
	cases := []struct {
		x, y float64
		want bool
	}{
		{cx, cy, true},                           // dead center
		{cx + l.Cfg.PlusRadius - 0.1, cy, true},  // just inside
		{cx + l.Cfg.PlusRadius + 0.1, cy, false}, // just outside
		{cx, cy + l.Cfg.PlusRadius, true},        // edge
		{cx + 100, cy + 100, false},              // far
		{cx - l.Cfg.PlusRadius/math.Sqrt2 + 0.1,
			cy - l.Cfg.PlusRadius/math.Sqrt2 + 0.1, true}, // diagonal inside
	}
	for _, c := range cases {
		got := l.PointInPlus(c.x, c.y)
		if got != c.want {
			t.Errorf("PointInPlus(%v,%v) = %v, want %v", c.x, c.y, got, c.want)
		}
	}
}

func TestTilePxFixedAcrossZoom(t *testing.T) {
	l := makeLayout()

	// Fixed at 3/4 of a default cell (CellPx 64 -> 48), regardless of zoom.
	for _, z := range []float64{1, 0.5, 10.0, 0} {
		l.PaneZoom = z
		if got := l.TilePx(); got != 48 {
			t.Errorf("zoom %v: TilePx = %v, want 48 (fixed)", z, got)
		}
	}
}

func TestPopoverAndTileRects(t *testing.T) {
	l := makeLayout()
	pop := l.PopoverRect()
	// 4 tiles, each 48 wide (fixed = CellPx*0.75), 5 gaps of 8 = 192+40 = 232.
	if pop.W != 232 {
		t.Errorf("PopoverRect width = %v, want 232", pop.W)
	}
	// Height = tile + 2*gap = 48+16 = 64.
	if pop.H != 64 {
		t.Errorf("PopoverRect height = %v, want 64", pop.H)
	}
	// Anchor: bottom-right corner of popover aligns with PlusRadius right of plus center.
	cx, cy := l.PlusCenter()
	if pop.X+pop.W != cx+l.Cfg.PlusRadius {
		t.Errorf("PopoverRect right edge: got %v, want %v", pop.X+pop.W, cx+l.Cfg.PlusRadius)
	}
	// Tile 0 starts at gap after popover.
	t0 := l.TileRect(0)
	if t0.X != pop.X+l.Cfg.GapPx || t0.Y != pop.Y+l.Cfg.GapPx {
		t.Errorf("TileRect(0) origin = (%v,%v), want (%v,%v)",
			t0.X, t0.Y, pop.X+l.Cfg.GapPx, pop.Y+l.Cfg.GapPx)
	}
	// Tiles are tile+gap apart.
	t1 := l.TileRect(1)
	if t1.X-t0.X != 56 {
		t.Errorf("TileRect spacing = %v, want 56 (48+8)", t1.X-t0.X)
	}
	_ = cy
}

func TestTileIndexAt(t *testing.T) {
	l := makeLayout()
	for i := range l.NumTiles {
		r := l.TileRect(i)
		got := l.TileIndexAt(r.X+r.W/2, r.Y+r.H/2)
		if got != i {
			t.Errorf("TileIndexAt center of tile %d: got %d", i, got)
		}
	}
	// In a gutter between two tiles.
	r0 := l.TileRect(0)
	gutterX := r0.X + r0.W + l.Cfg.GapPx/2
	if got := l.TileIndexAt(gutterX, r0.Y+r0.H/2); got != -1 {
		t.Errorf("TileIndexAt gutter: got %d, want -1", got)
	}
	// Outside popover.
	if got := l.TileIndexAt(0, 0); got != -1 {
		t.Errorf("TileIndexAt outside: got %d, want -1", got)
	}
}

func TestLayoutHandlesExpandedKindCount(t *testing.T) {
	// The current palette ships six kinds (well, markdown, url, file-well,
	// process-well, shell). The layout has to widen the popover to fit them
	// and the tile rects must stay non-overlapping.
	l := makeLayout()
	l.NumTiles = 6
	pop := l.PopoverRect()
	tile := l.TilePx()
	wantW := float64(6)*tile + float64(6+1)*l.Cfg.GapPx
	if pop.W != wantW {
		t.Errorf("PopoverRect.W = %v, want %v", pop.W, wantW)
	}
	for i := 1; i < 6; i++ {
		a := l.TileRect(i - 1)
		b := l.TileRect(i)
		if a.X+a.W > b.X {
			t.Errorf("tile %d overlaps tile %d: a.X+W=%v, b.X=%v",
				i-1, i, a.X+a.W, b.X)
		}
	}
	last := l.NumTiles - 1
	if got := l.TileIndexAt(l.TileRect(last).X+l.TileRect(last).W/2, l.TileRect(last).Y+l.TileRect(last).H/2); got != last {
		t.Errorf("TileIndexAt center of last tile: got %d, want %d", got, last)
	}
}

func TestPointInPopover(t *testing.T) {
	l := makeLayout()
	pop := l.PopoverRect()
	if !l.PointInPopover(pop.X+pop.W/2, pop.Y+pop.H/2) {
		t.Error("center of popover should be inside")
	}
	if l.PointInPopover(pop.X-10, pop.Y-10) {
		t.Error("above-left of popover should not be inside")
	}
	if !l.PointInPopover(pop.X, pop.Y) {
		t.Error("popover corner should count as inside")
	}
}

func TestTrackingPaneSize(t *testing.T) {
	// + button stays anchored to bottom-right as pane grows.
	l := makeLayout()
	cx1, cy1 := l.PlusCenter()
	l.Pane.W += 500
	l.Pane.H += 500
	cx2, cy2 := l.PlusCenter()
	if cx2-cx1 != 500 || cy2-cy1 != 500 {
		t.Errorf("PlusCenter delta = (%v,%v), want (500,500)", cx2-cx1, cy2-cy1)
	}
}

// TestTwoRowPopover: plugins fill the top row, primitives the bottom. The
// popover is two tiles tall and the bottom-row tiles sit below the top.
func TestTwoRowPopover(t *testing.T) {
	l := makeLayout()
	l.NumTiles = 6
	l.TopRow = 2
	tile := l.TilePx()
	gap := l.Cfg.GapPx
	if got, want := l.PopoverRect().H, 2*tile+3*gap; got != want {
		t.Errorf("popover height = %v, want %v (two rows)", got, want)
	}
	top := l.TileRect(0)
	bottom := l.TileRect(2)
	if bottom.Y <= top.Y {
		t.Errorf("bottom-row Y %v not below top-row Y %v", bottom.Y, top.Y)
	}
	// Index round-trips through both rows.
	for i := range l.NumTiles {
		r := l.TileRect(i)
		if got := l.TileIndexAt(r.X+r.W/2, r.Y+r.H/2); got != i {
			t.Errorf("TileIndexAt(center of %d) = %d", i, got)
		}
	}
}

// TestSingleRowWhenTopRowCoversAll: when every tile is a plugin (TopRow ==
// NumTiles, the read-only-grid case) the popover stays a single row.
func TestSingleRowWhenTopRowCoversAll(t *testing.T) {
	l := makeLayout()
	l.NumTiles = 3
	l.TopRow = 3
	tile := l.TilePx()
	gap := l.Cfg.GapPx
	if got, want := l.PopoverRect().H, tile+2*gap; got != want {
		t.Errorf("popover height = %v, want %v (single row)", got, want)
	}
}

// TestLauncherCellsCentered: launcher tiles form a single row of cells
// centered on the origin (so the row sits at the pane's view center), and the
// cell-space index round-trips.
func TestLauncherCellsCentered(t *testing.T) {
	n := 3
	first := LauncherCellRect(0, n)
	last := LauncherCellRect(n-1, n)
	// Midpoint of the first and last tile centers is the origin.
	if mid := (first.X + first.W/2 + last.X + last.W/2) / 2; mid != 0 {
		t.Errorf("row midpoint = %v, want 0 (centered on origin)", mid)
	}
	// Tiles are vertically centered on row 0.
	if first.Y+first.H/2 != 0 {
		t.Errorf("tile Y-center = %v, want 0", first.Y+first.H/2)
	}
	for i := range n {
		r := LauncherCellRect(i, n)
		if got := LauncherCellIndexAt(r.X+r.W/2, r.Y+r.H/2, n); got != i {
			t.Errorf("LauncherCellIndexAt(center of %d) = %d", i, got)
		}
	}
	// A point well off the row resolves to nothing.
	if got := LauncherCellIndexAt(100, 100, n); got != -1 {
		t.Errorf("LauncherCellIndexAt(far) = %d, want -1", got)
	}
}
