package palette

import (
	"testing"
)

func makeLayout() Layout {
	return Layout{
		Cfg:      Default(),
		PlusX:    976,
		PlusY:    776,
		NumTiles: 4,
	}
}

func TestPlusCenter(t *testing.T) {
	l := makeLayout()
	cx, cy := l.PlusCenter()
	if cx != 976 || cy != 776 {
		t.Errorf("PlusCenter = (%v,%v), want the caller's (976,776) verbatim", cx, cy)
	}
}

func TestTilePxFixed(t *testing.T) {
	// Fixed at 3/4 of a default cell (CellPx 64 -> 48): the creation menu is
	// a constant-size affordance.
	if got := makeLayout().TilePx(); got != 48 {
		t.Errorf("TilePx = %v, want 48 (fixed)", got)
	}
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
	// With six tiles the layout has to widen the popover to fit them, and
	// the tile rects must stay non-overlapping.
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

func TestPopoverTracksCenter(t *testing.T) {
	// The popover is anchored to the + center: moving the center (a window
	// resize moves the bar slot) translates the popover one for one.
	l := makeLayout()
	r1 := l.PopoverRect()
	l.PlusX += 500
	l.PlusY += 300
	r2 := l.PopoverRect()
	if r2.X-r1.X != 500 || r2.Y-r1.Y != 300 {
		t.Errorf("PopoverRect delta = (%v,%v), want (500,300)", r2.X-r1.X, r2.Y-r1.Y)
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

// TestSingleRowWhenTopRowUnset: with no TopRow split (no plugins configured)
// the popover stays a single row.
func TestSingleRowWhenTopRowUnset(t *testing.T) {
	l := makeLayout()
	tile := l.TilePx()
	gap := l.Cfg.GapPx
	wantH := tile + 2*gap
	for _, n := range []int{1, 2, 4, 6, 8} {
		l.NumTiles = n
		if got := l.PopoverRect().H; got != wantH {
			t.Errorf("NumTiles=%d: PopoverRect.H = %v, want %v (single row)", n, got, wantH)
		}
		y0 := l.TileRect(0).Y
		for i := 1; i < n; i++ {
			if got := l.TileRect(i).Y; got != y0 {
				t.Errorf("NumTiles=%d tile %d: Y=%v differs from tile 0 Y=%v", n, i, got, y0)
			}
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
