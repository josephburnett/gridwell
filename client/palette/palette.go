// Package palette computes the layout of Gridwell's tile-creation
// palette — the popover that opens over the pane's "+" button and
// holds the swatches the user drags onto the canvas: the configured
// plugins on a top row (click to enter, drag to drop a link), the
// tile primitives (well, markdown, url, shell, pane) on a row below.
//
// All layout is pure: it depends only on the pane's screen rect, the
// pane's current zoom, and the tile counts. The wasm renderer reads
// the rects out and paints into them.
package palette

import "github.com/josephburnett/gridwell/client/pane"

// Rect is pane.Rect — one screen-space rectangle type for the whole client.
type Rect = pane.Rect

// Config is the tunable layout for the + button and palette popover.
// Defaults match the renderer's current constants; callers can change
// them in tests.
type Config struct {
	// PlusInset is the distance in screen pixels from the pane's
	// bottom-right corner to the center of the + button.
	PlusInset float64
	// PlusRadius is the + button's hit-test radius (and visual radius).
	PlusRadius float64
	// TileMinPx, TileMaxPx are the clamp limits on the per-tile size
	// in the popover, in screen pixels.
	TileMinPx, TileMaxPx float64
	// GapPx is the gutter between tiles (and between tiles and the
	// popover border).
	GapPx float64
	// CellPx is the renderer's base cell size at zoom 1.0; the palette
	// tile size tracks paneZoom*CellPx so the preview matches the
	// to-be-placed tile.
	CellPx float64
}

// Default returns the layout constants currently used by the wasm
// renderer. Centralizing them here means the tests pin the same
// values the user sees.
func Default() Config {
	return Config{
		PlusInset:  24,
		PlusRadius: 18,
		TileMinPx:  48,
		TileMaxPx:  128,
		GapPx:      8,
		CellPx:     pane.CellPx,
	}
}

// Layout snapshots one palette's input. All methods are pure and
// don't allocate.
type Layout struct {
	Cfg      Config
	Pane     Rect
	PaneZoom float64
	NumTiles int
	// TopRow is how many of NumTiles sit in the popover's first row (the
	// plugins); the rest (the primitives) go in a second row below. When
	// TopRow is unset (<=0) or covers every tile, the popover is a single
	// row.
	TopRow int
}

// topCount / bottomCount split NumTiles across the two popover rows. A TopRow
// that is unset or covers everything collapses to a single row.
func (l Layout) topCount() int {
	if l.TopRow <= 0 || l.TopRow >= l.NumTiles {
		return l.NumTiles
	}
	return l.TopRow
}

func (l Layout) bottomCount() int { return l.NumTiles - l.topCount() }

func (l Layout) rowCount() int {
	if l.bottomCount() > 0 {
		return 2
	}
	return 1
}

// rowWidthPx is the popover width a row of n tiles needs (tiles + gutters).
func (l Layout) rowWidthPx(n int) float64 {
	return float64(n)*l.TilePx() + float64(n+1)*l.Cfg.GapPx
}

// PlusCenter returns the screen-space center of the + button (the pane's
// lower-right corner, inset by PlusInset).
func (l Layout) PlusCenter() (cx, cy float64) {
	return l.Pane.X + l.Pane.W - l.Cfg.PlusInset, l.Pane.Y + l.Pane.H - l.Cfg.PlusInset
}

// PointInPlus reports whether (x, y) lies inside the + button's hit
// circle.
func (l Layout) PointInPlus(x, y float64) bool {
	cx, cy := l.PlusCenter()
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= l.Cfg.PlusRadius*l.Cfg.PlusRadius
}

// TilePx returns the per-tile size in screen pixels for the palette.
// Fixed at half a default cell and independent of pane zoom: the creation
// menu is a constant-size affordance — a row of icons, not a literal
// preview of the placed tile's on-screen size. (The drag ghost resizes to
// the destination zoom on drop, the same as dragging a tile across wells.)
func (l Layout) TilePx() float64 {
	return l.Cfg.CellPx * 0.75
}

// PopoverRect returns the screen rect of the entire palette popover,
// anchored just above the + button. Wide enough for the wider of the two
// rows; tall enough for however many rows (1 or 2) are populated.
func (l Layout) PopoverRect() Rect {
	tile := l.TilePx()
	w := max(l.rowWidthPx(l.topCount()), l.rowWidthPx(l.bottomCount()))
	rows := float64(l.rowCount())
	h := rows*tile + (rows+1)*l.Cfg.GapPx
	cx, cy := l.PlusCenter()
	x := cx + l.Cfg.PlusRadius - w
	y := cy - l.Cfg.PlusRadius - h - 8
	return Rect{X: x, Y: y, W: w, H: h}
}

// TileRect returns the screen rect of the i'th template tile inside the
// popover. Tiles 0..topCount-1 fill the top row; the rest fill the bottom
// row. Each row is centered horizontally within the popover so a short row
// sits under the middle of a wider one.
func (l Layout) TileRect(i int) Rect {
	pop := l.PopoverRect()
	tile := l.TilePx()
	gap := l.Cfg.GapPx
	row, col, count := 0, i, l.topCount()
	if i >= l.topCount() {
		row, col, count = 1, i-l.topCount(), l.bottomCount()
	}
	rowX := pop.X + (pop.W-l.rowWidthPx(count))/2
	return Rect{
		X: rowX + gap + float64(col)*(tile+gap),
		Y: pop.Y + gap + float64(row)*(tile+gap),
		W: tile,
		H: tile,
	}
}

// TileIndexAt returns the index of the template tile under (x, y), or
// -1 if the point is in a gutter or outside the popover.
func (l Layout) TileIndexAt(x, y float64) int {
	for i := range l.NumTiles {
		r := l.TileRect(i)
		if x >= r.X && x <= r.X+r.W && y >= r.Y && y <= r.Y+r.H {
			return i
		}
	}
	return -1
}

// PointInPopover reports whether (x, y) is anywhere inside the popover
// rect (tiles or gutter). Used to keep the palette open when the user
// clicks inside but misses a tile.
func (l Layout) PointInPopover(x, y float64) bool {
	r := l.PopoverRect()
	return x >= r.X && x <= r.X+r.W && y >= r.Y && y <= r.Y+r.H
}
