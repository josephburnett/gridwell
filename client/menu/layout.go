package menu

// The + menu's geometry. The strip, the templates and then one swatch per
// declared door (Doors), sits above the tile primitives' row, and a chevron
// band folds it away (Show). Layout depends only on the + button's center,
// the item counts and the strip's scroll; the wasm renderer paints into the
// rects.

import "github.com/josephburnett/gridwell/client/pane"

type Rect = pane.Rect

// Config is the tunable layout for the + button and the popover.
type Config struct {
	// PlusRadius fits inside the bottom bar's band (wsbar.RowH) with margin.
	PlusRadius           float64
	TileMinPx, TileMaxPx float64
	GapPx                float64
	CellPx               float64
	// ToggleH is the chevron strip's height. The strip is a control the
	// user presses, not a swatch, so it is not sized as one.
	ToggleH float64
}

// Default is the constants the wasm renderer uses, so a test pins the values
// the user sees.
func Default() Config {
	return Config{
		PlusRadius: 14,
		TileMinPx:  48,
		TileMaxPx:  128,
		GapPx:      8,
		CellPx:     pane.CellPx,
		ToggleH:    18,
	}
}

// RowMax is the most items a row of the popover holds: the strip wraps at it,
// over the five Gridwell primitives.
const RowMax = 5

// StripRowsShown is how many strip rows the popover shows; the rest scroll.
const StripRowsShown = 3

// Layout snapshots one menu's input. Every method is pure.
type Layout struct {
	Cfg          Config
	PlusX, PlusY float64
	NumTiles     int
	// Sections is the strip's item counts, one per section in display order
	// (the templates, then the swatches); the strip holds the first of
	// NumTiles and the primitives the rest. A section wraps at RowMax and
	// never shares a row with another, and an empty one takes no row.
	Sections []int
	// Toggle is Show's answer. The strip sits above the primitives in either
	// fold state.
	Toggle bool
	// Scroll is how far down the strip is scrolled, in px; read through
	// ClampScroll, so any value is safe.
	Scroll float64
}

// stripRow is one row of the strip: the first item's index and how many.
type stripRow struct{ start, n int }

func (l Layout) stripRows() []stripRow {
	var rows []stripRow
	start := 0
	for _, n := range l.Sections {
		n = min(n, l.NumTiles-start)
		for i := 0; i < n; i += RowMax {
			rows = append(rows, stripRow{start + i, min(RowMax, n-i)})
		}
		start += max(n, 0)
	}
	return rows
}

func (l Layout) stripCount() int {
	n := 0
	for _, r := range l.stripRows() {
		n += r.n
	}
	return n
}

func (l Layout) bottomCount() int { return l.NumTiles - l.stripCount() }

func (l Layout) shownStripRows() int { return min(len(l.stripRows()), StripRowsShown) }

func (l Layout) pitch() float64 { return l.TilePx() + l.Cfg.GapPx }

func (l Layout) toggleBand() float64 {
	if !l.Toggle {
		return 0
	}
	return l.Cfg.ToggleH + l.Cfg.GapPx
}

// stripTop is the y of the strip's first row unscrolled.
func (l Layout) stripTop() float64 { return l.PopoverRect().Y + l.Cfg.GapPx }

func (l Layout) bottomY() float64 {
	return l.stripTop() + float64(l.shownStripRows())*l.pitch() + l.toggleBand()
}

func (l Layout) rowWidthPx(n int) float64 {
	return float64(n)*l.TilePx() + float64(n+1)*l.Cfg.GapPx
}

// ClampScroll bounds a strip scroll to the rows past the shown ones.
func (l Layout) ClampScroll(s float64) float64 {
	limit := float64(len(l.stripRows())-l.shownStripRows()) * l.pitch()
	return min(max(s, 0), limit)
}

// StripRect is the strip's visible band: a strip item is drawn clipped to it
// and hit only inside it. The zero rect when the strip shows nothing.
func (l Layout) StripRect() Rect {
	rows := l.shownStripRows()
	if rows == 0 {
		return Rect{}
	}
	pop := l.PopoverRect()
	return Rect{X: pop.X, Y: l.stripTop(), W: pop.W, H: float64(rows)*l.pitch() - l.Cfg.GapPx}
}

func (l Layout) PlusCenter() (cx, cy float64) {
	return l.PlusX, l.PlusY
}

// TilePx is independent of pane zoom, so the menu is the same size wherever it
// opens. The drag ghost resizes to the destination zoom on drop.
func (l Layout) TilePx() float64 {
	return l.Cfg.CellPx * 0.75
}

// PopoverRect is anchored above the + button.
func (l Layout) PopoverRect() Rect {
	w := l.rowWidthPx(l.bottomCount())
	for _, r := range l.stripRows() {
		w = max(w, l.rowWidthPx(r.n))
	}
	rows := l.shownStripRows()
	if l.bottomCount() > 0 {
		rows++
	}
	h := float64(rows)*l.TilePx() + float64(rows+1)*l.Cfg.GapPx + l.toggleBand()
	cx, cy := l.PlusCenter()
	x := cx + l.Cfg.PlusRadius - w
	y := cy - l.Cfg.PlusRadius - h - 8
	return Rect{X: x, Y: y, W: w, H: h}
}

// TileRect centers each row in the popover, so a short row sits under the
// middle of a wider one. A strip item's rect is scrolled, and may lie outside
// StripRect.
func (l Layout) TileRect(i int) Rect {
	pop := l.PopoverRect()
	tile := l.TilePx()
	gap := l.Cfg.GapPx
	y, col, count := l.bottomY(), i-l.stripCount(), l.bottomCount()
	for r, row := range l.stripRows() {
		if i >= row.start && i < row.start+row.n {
			y = l.stripTop() + float64(r)*l.pitch() - l.ClampScroll(l.Scroll)
			col, count = i-row.start, row.n
		}
	}
	rowX := pop.X + (pop.W-l.rowWidthPx(count))/2
	return Rect{X: rowX + gap + float64(col)*(tile+gap), Y: y, W: tile, H: tile}
}

// ToggleRect is the zero rect when the popover has no toggle.
func (l Layout) ToggleRect() Rect {
	if !l.Toggle {
		return Rect{}
	}
	pop := l.PopoverRect()
	gap := l.Cfg.GapPx
	return Rect{
		X: pop.X + gap,
		Y: l.bottomY() - l.toggleBand(),
		W: pop.W - 2*gap,
		H: l.Cfg.ToggleH,
	}
}

// PointInToggle is false with no toggle, so a caller needs no second guard.
func (l Layout) PointInToggle(x, y float64) bool {
	if !l.Toggle {
		return false
	}
	return l.ToggleRect().Contains(x, y)
}

// TileIndexAt is -1 off an item, and off a strip item scrolled out of
// StripRect.
func (l Layout) TileIndexAt(x, y float64) int {
	strip := l.stripCount()
	for i := range l.NumTiles {
		if l.TileRect(i).Contains(x, y) && (i >= strip || l.StripRect().Contains(x, y)) {
			return i
		}
	}
	return -1
}

// PointInPopover: a click that misses a swatch but lands here keeps the menu
// open.
func (l Layout) PointInPopover(x, y float64) bool {
	return l.PopoverRect().Contains(x, y)
}
