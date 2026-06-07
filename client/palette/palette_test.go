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
		{cx, cy, true},                                 // dead center
		{cx + l.Cfg.PlusRadius - 0.1, cy, true},        // just inside
		{cx + l.Cfg.PlusRadius + 0.1, cy, false},       // just outside
		{cx, cy + l.Cfg.PlusRadius, true},              // edge
		{cx + 100, cy + 100, false},                    // far
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

func TestTilePxClamping(t *testing.T) {
	l := makeLayout()

	// At zoom 1, CellPx*1 = 64, between 48 and 128 -> 64.
	if got := l.TilePx(); got != 64 {
		t.Errorf("zoom 1: TilePx = %v, want 64", got)
	}

	l.PaneZoom = 0.5
	// 64*0.5 = 32 → clamped up to 48.
	if got := l.TilePx(); got != 48 {
		t.Errorf("zoom 0.5: TilePx = %v, want 48", got)
	}

	l.PaneZoom = 10.0
	// 64*10 = 640 → clamped down to 128.
	if got := l.TilePx(); got != 128 {
		t.Errorf("zoom 10: TilePx = %v, want 128", got)
	}

	l.PaneZoom = 0
	// 0 zoom is degenerate -> treated as 1 -> 64.
	if got := l.TilePx(); got != 64 {
		t.Errorf("zoom 0: TilePx = %v, want 64", got)
	}
}

func TestPopoverAndTileRects(t *testing.T) {
	l := makeLayout()
	pop := l.PopoverRect()
	// 4 tiles, each 64 wide, 5 gaps of 8 = 256+40 = 296.
	if pop.W != 296 {
		t.Errorf("PopoverRect width = %v, want 296", pop.W)
	}
	// Height = tile + 2*gap = 64+16 = 80.
	if pop.H != 80 {
		t.Errorf("PopoverRect height = %v, want 80", pop.H)
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
	if t1.X-t0.X != 72 {
		t.Errorf("TileRect spacing = %v, want 72 (64+8)", t1.X-t0.X)
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
	// The current palette ships seven kinds (well, markdown, url,
	// blackhole, file-well, process-well, shell). The layout has to
	// widen the popover to fit them and the tile rects must stay
	// non-overlapping.
	l := makeLayout()
	l.NumTiles = 7
	pop := l.PopoverRect()
	tile := l.TilePx()
	wantW := float64(7)*tile + float64(7+1)*l.Cfg.GapPx
	if pop.W != wantW {
		t.Errorf("PopoverRect.W = %v, want %v", pop.W, wantW)
	}
	for i := 1; i < 7; i++ {
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
