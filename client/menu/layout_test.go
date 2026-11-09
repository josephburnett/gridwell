package menu

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
	// Three quarters of a default cell: CellPx 64 gives 48.
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
	// Six tiles widen the popover, and the rects stay non-overlapping.
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
	// Half-open, like every other rect in the client: the far edge belongs
	// to whatever is past it.
	if l.PointInPopover(pop.X+pop.W, pop.Y) {
		t.Error("the popover's far edge is outside it")
	}
	r0 := l.TileRect(0)
	if l.TileIndexAt(r0.X+r0.W, r0.Y+r0.H/2) != -1 {
		t.Error("a swatch's far edge is not on the swatch")
	}
}

func TestPopoverTracksCenter(t *testing.T) {
	// The popover is anchored to the + center, so moving the center
	// translates it one for one.
	l := makeLayout()
	r1 := l.PopoverRect()
	l.PlusX += 500
	l.PlusY += 300
	r2 := l.PopoverRect()
	if r2.X-r1.X != 500 || r2.Y-r1.Y != 300 {
		t.Errorf("PopoverRect delta = (%v,%v), want (500,300)", r2.X-r1.X, r2.Y-r1.Y)
	}
}

// TestTwoRowPopover pins that doors fill the top row and primitives the
// bottom, two tiles tall.
func TestTwoRowPopover(t *testing.T) {
	l := makeLayout()
	l.NumTiles = 6
	l.Sections = []int{2}
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
	for i := range l.NumTiles {
		r := l.TileRect(i)
		if got := l.TileIndexAt(r.X+r.W/2, r.Y+r.H/2); got != i {
			t.Errorf("TileIndexAt(center of %d) = %d", i, got)
		}
	}
}

// TestSingleRowWithNoStrip pins that a node declaring no doors gets a
// single-row popover.
func TestSingleRowWithNoStrip(t *testing.T) {
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

// TestSingleRowWhenTheStripCoversAll pins the read-only grid, where every tile
// is a door and the popover stays a single row.
func TestSingleRowWhenTheStripCoversAll(t *testing.T) {
	l := makeLayout()
	l.NumTiles = 3
	l.Sections = []int{3}
	tile := l.TilePx()
	gap := l.Cfg.GapPx
	if got, want := l.PopoverRect().H, tile+2*gap; got != want {
		t.Errorf("popover height = %v, want %v (single row)", got, want)
	}
}

// TestCollapsedSectionLayout pins the folded popover: the primitives row
// with the strip above it, inside the popover and on no tile.
func TestCollapsedSectionLayout(t *testing.T) {
	l := makeLayout()
	l.NumTiles = 4 // primitives only: the section contributed nothing
	l.Sections = []int{0}
	l.Toggle = true
	tile := l.TilePx()
	gap := l.Cfg.GapPx
	pop := l.PopoverRect()
	if got, want := pop.H, tile+2*gap+l.Cfg.ToggleH+gap; got != want {
		t.Errorf("popover height = %v, want %v (one row plus the strip)", got, want)
	}
	tr := l.ToggleRect()
	if tr.Y != pop.Y+gap {
		t.Errorf("strip Y = %v, want the popover's first band %v", tr.Y, pop.Y+gap)
	}
	if got, want := tr.W, pop.W-2*gap; got != want {
		t.Errorf("strip width = %v, want the popover width less the gutters %v", got, want)
	}
	if first := l.TileRect(0); first.Y < tr.Y+tr.H {
		t.Errorf("primitives row Y %v overlaps the strip ending at %v", first.Y, tr.Y+tr.H)
	}
	// The strip is hit-tested as itself rather than as a swatch.
	cx, cy := tr.X+tr.W/2, tr.Y+tr.H/2
	if !l.PointInToggle(cx, cy) {
		t.Error("the strip center should hit the toggle")
	}
	if got := l.TileIndexAt(cx, cy); got != -1 {
		t.Errorf("TileIndexAt on the strip = %d, want -1 (not a swatch)", got)
	}
	if !l.PointInPopover(cx, cy) {
		t.Error("the strip should be inside the popover, so a press there keeps it open")
	}
	// Every swatch round-trips through the shifted geometry.
	for i := range l.NumTiles {
		r := l.TileRect(i)
		if got := l.TileIndexAt(r.X+r.W/2, r.Y+r.H/2); got != i {
			t.Errorf("TileIndexAt(center of %d) = %d", i, got)
		}
		if l.PointInToggle(r.X+r.W/2, r.Y+r.H/2) {
			t.Errorf("swatch %d center also hits the toggle", i)
		}
	}
}

// TestExpandedSectionLayout pins that the strip sits between the two rows
// and both keep their order and hit-testing.
func TestExpandedSectionLayout(t *testing.T) {
	l := makeLayout()
	l.NumTiles = 7
	l.Sections = []int{3}
	l.Toggle = true
	tile := l.TilePx()
	gap := l.Cfg.GapPx
	if got, want := l.PopoverRect().H, 2*tile+3*gap+l.Cfg.ToggleH+gap; got != want {
		t.Errorf("popover height = %v, want %v (two rows plus the strip)", got, want)
	}
	top := l.TileRect(0)
	tr := l.ToggleRect()
	bottom := l.TileRect(3)
	if tr.Y < top.Y+top.H {
		t.Errorf("strip Y %v is not below the source row ending at %v", tr.Y, top.Y+top.H)
	}
	if bottom.Y < tr.Y+tr.H {
		t.Errorf("primitives row Y %v is not below the strip ending at %v", bottom.Y, tr.Y+tr.H)
	}
	for i := range l.NumTiles {
		r := l.TileRect(i)
		if got := l.TileIndexAt(r.X+r.W/2, r.Y+r.H/2); got != i {
			t.Errorf("TileIndexAt(center of %d) = %d", i, got)
		}
	}
}

// TestNoToggleNoBand pins that a popover without the strip costs no extra
// space.
func TestNoToggleNoBand(t *testing.T) {
	l := makeLayout()
	l.NumTiles = 7
	l.Sections = []int{3}
	tile := l.TilePx()
	gap := l.Cfg.GapPx
	if got, want := l.PopoverRect().H, 2*tile+3*gap; got != want {
		t.Errorf("popover height = %v, want %v (no strip band)", got, want)
	}
	if got := l.ToggleRect(); got != (Rect{}) {
		t.Errorf("ToggleRect = %+v, want the zero rect with no toggle", got)
	}
	if l.PointInToggle(l.PopoverRect().X+1, l.PopoverRect().Y+1) {
		t.Error("PointInToggle must be false with no toggle")
	}
}

// TestStripWrapsEachSection pins the strip's rows: each section wraps at
// RowMax and never shares a row with the next, templates above swatches.
func TestStripWrapsEachSection(t *testing.T) {
	l := makeLayout()
	l.Sections = []int{2, 7} // [+] and one template; seven swatches
	l.NumTiles = 9 + 5
	l.Toggle = true
	rowOf := func(i int) float64 { return l.TileRect(i).Y }
	if rowOf(0) != rowOf(1) {
		t.Error("the templates section's two items share a row")
	}
	if rowOf(2) <= rowOf(1) {
		t.Errorf("the swatches start a row of their own: swatch row %v, templates row %v", rowOf(2), rowOf(1))
	}
	for i := 3; i < 7; i++ {
		if rowOf(i) != rowOf(2) {
			t.Errorf("swatch %d is not on the first swatch row", i-2)
		}
	}
	if rowOf(7) <= rowOf(6) || rowOf(8) != rowOf(7) {
		t.Errorf("the sixth and seventh swatches wrap onto the next row: %v %v %v", rowOf(6), rowOf(7), rowOf(8))
	}
	if got, want := l.PopoverRect().W, l.rowWidthPx(RowMax); got != want {
		t.Errorf("popover width = %v, want a row of %d, %v", got, RowMax, want)
	}
	tile, gap := l.TilePx(), l.Cfg.GapPx
	if got, want := l.PopoverRect().H, 4*tile+5*gap+l.Cfg.ToggleH+gap; got != want {
		t.Errorf("popover height = %v, want three strip rows, the band and the primitives, %v", got, want)
	}
	if prim := l.TileRect(9); prim.Y < l.ToggleRect().Y+l.ToggleRect().H {
		t.Error("the primitives row is not below the band")
	}
}

// TestStripScrollsPastThreeRows pins that the strip shows StripRowsShown
// rows, a scroll is clamped to the rows past them, and the hit test follows
// the scroll and ignores an item scrolled out.
func TestStripScrollsPastThreeRows(t *testing.T) {
	l := makeLayout()
	l.Sections = []int{11, 3} // three template rows, one swatch row
	l.NumTiles = 14 + 5
	pitch := l.TilePx() + l.Cfg.GapPx
	if got := l.ClampScroll(-5); got != 0 {
		t.Errorf("ClampScroll(-5) = %v, want 0", got)
	}
	if got := l.ClampScroll(1e6); got != pitch {
		t.Errorf("ClampScroll(far) = %v, want one row, %v", got, pitch)
	}
	strip := l.StripRect()
	if got, want := strip.H, 3*pitch-l.Cfg.GapPx; got != want {
		t.Errorf("strip band height = %v, want three rows %v", got, want)
	}
	swatch := l.TileRect(11)
	if l.TileIndexAt(swatch.X+swatch.W/2, swatch.Y+swatch.H/2) == 11 {
		t.Error("the fourth row is hit before it is scrolled into the band")
	}
	l.Scroll = 1e6
	swatch = l.TileRect(11)
	if got := l.TileIndexAt(swatch.X+swatch.W/2, swatch.Y+swatch.H/2); got != 11 {
		t.Errorf("TileIndexAt the scrolled-in swatch = %d, want 11", got)
	}
	first := l.TileRect(0)
	if l.TileIndexAt(first.X+first.W/2, first.Y+first.H/2) != -1 {
		t.Error("the first row is hit after it scrolled out of the band")
	}
	prim := l.TileRect(14)
	if got := l.TileIndexAt(prim.X+prim.W/2, prim.Y+prim.H/2); got != 14 {
		t.Errorf("the primitives do not scroll: TileIndexAt = %d, want 14", got)
	}
	l.Sections = []int{2}
	l.NumTiles = 7
	if got := l.ClampScroll(50); got != 0 {
		t.Errorf("a strip that fits does not scroll: ClampScroll = %v", got)
	}
}
