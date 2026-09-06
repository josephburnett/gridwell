// Package palette computes the layout of Gridwell's tile-creation
// palette — the popover that opens over the pane's "+" button and
// holds the swatches the user drags onto the canvas: the configured
// plugins on a top row (click to enter, drag to drop a link), the
// tile primitives (well, markdown, url, shell, pane) on a row below.
// The plugin row folds away behind a disclosure strip — see section.go
// for which rows a given state shows.
//
// All layout is pure: it depends only on the + button's center (the bottom
// bar's right-end slot) and the tile counts.
// The wasm renderer reads the rects out and paints into them.
package palette

import "github.com/josephburnett/gridwell/client/pane"

// Rect is pane.Rect — one screen-space rectangle type for the whole client.
type Rect = pane.Rect

// Config is the tunable layout for the + button and palette popover.
// Defaults match the renderer's current constants; callers can change
// them in tests.
type Config struct {
	// PlusRadius is the + button's hit-test radius (and visual radius).
	// Sized to fit inside the bottom bar's band (wsbar.RowH) with margin.
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
	// ToggleH is the height of the plugin section's disclosure strip, the
	// full-popover-width band carrying the chevron. It is deliberately not
	// a tile: a swatch is a template you drag, and this is a control you
	// press.
	ToggleH float64
}

// Default returns the layout constants currently used by the wasm
// renderer. Centralizing them here means the tests pin the same
// values the user sees.
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

// Layout snapshots one palette's input. All methods are pure and
// don't allocate.
type Layout struct {
	Cfg Config
	// PlusX, PlusY are the + button's center: the bottom bar's right-end
	// slot, a fixed home.
	PlusX, PlusY float64
	NumTiles     int
	// TopRow is how many of NumTiles sit in the popover's first row (the
	// plugin section); the rest (the primitives) go in a row below. Either
	// count may be zero — a node with no plugins, or a section folded away —
	// and then the populated row is the only one.
	TopRow int
	// Toggle is whether the plugin section's disclosure strip is in the
	// popover. Show decides that; the strip sits between the two rows, which
	// is above the primitives whichever state the section is in.
	Toggle bool
}

// topCount / bottomCount split NumTiles across the two popover rows.
func (l Layout) topCount() int {
	if l.TopRow <= 0 {
		return 0
	}
	return min(l.TopRow, l.NumTiles)
}

func (l Layout) bottomCount() int { return l.NumTiles - l.topCount() }

func (l Layout) rowCount() int {
	n := 0
	if l.topCount() > 0 {
		n++
	}
	if l.bottomCount() > 0 {
		n++
	}
	return n
}

// toggleRow is the row index the disclosure strip sits above: 1 when the
// plugin row is populated, 0 when it is not. Rows from there down are pushed
// by the strip's band.
func (l Layout) toggleRow() int {
	if l.topCount() > 0 {
		return 1
	}
	return 0
}

// toggleBand is the vertical space the strip takes, zero when there is none.
func (l Layout) toggleBand() float64 {
	if !l.Toggle {
		return 0
	}
	return l.Cfg.ToggleH + l.Cfg.GapPx
}

// rowY is the top of the given popover row, counting the strip's band for
// every row at or below it.
func (l Layout) rowY(row int) float64 {
	y := l.PopoverRect().Y + l.Cfg.GapPx + float64(row)*(l.TilePx()+l.Cfg.GapPx)
	if row >= l.toggleRow() {
		y += l.toggleBand()
	}
	return y
}

// rowWidthPx is the popover width a row of n tiles needs (tiles + gutters).
func (l Layout) rowWidthPx(n int) float64 {
	return float64(n)*l.TilePx() + float64(n+1)*l.Cfg.GapPx
}

// PlusCenter returns the screen-space center of the + button.
func (l Layout) PlusCenter() (cx, cy float64) {
	return l.PlusX, l.PlusY
}

// TilePx returns the per-tile size in screen pixels for the palette. Fixed
// at three quarters of a default cell and independent of pane zoom: the
// creation menu is a constant-size affordance — a row of icons, not a
// literal preview of the placed tile's on-screen size. The drag ghost
// resizes to the destination zoom on drop, the same as dragging a tile
// across wells.
func (l Layout) TilePx() float64 {
	return l.Cfg.CellPx * 0.75
}

// PopoverRect returns the screen rect of the entire palette popover,
// anchored just above the + button. Wide enough for the wider of the two
// rows; tall enough for however many rows (0, 1 or 2) are populated, plus the
// disclosure strip's band when the popover carries one.
func (l Layout) PopoverRect() Rect {
	tile := l.TilePx()
	w := max(l.rowWidthPx(l.topCount()), l.rowWidthPx(l.bottomCount()))
	rows := float64(l.rowCount())
	h := rows*tile + (rows+1)*l.Cfg.GapPx + l.toggleBand()
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
	top := l.topCount()
	row, col, count := 0, i, top
	if i >= top {
		// The primitives' row is the second one only when the plugin row
		// above it is populated; folded away, they are the popover's first.
		row, col, count = l.toggleRow(), i-top, l.bottomCount()
	}
	rowX := pop.X + (pop.W-l.rowWidthPx(count))/2
	return Rect{
		X: rowX + gap + float64(col)*(tile+gap),
		Y: l.rowY(row),
		W: tile,
		H: tile,
	}
}

// ToggleRect returns the screen rect of the plugin section's disclosure
// strip: the full popover width, in the band between the section and the
// primitives. The zero rect when the popover has no toggle.
func (l Layout) ToggleRect() Rect {
	if !l.Toggle {
		return Rect{}
	}
	pop := l.PopoverRect()
	gap := l.Cfg.GapPx
	// The strip's own row is the band rowY reserves for it: the top of the
	// row it precedes, less the band.
	return Rect{
		X: pop.X + gap,
		Y: l.rowY(l.toggleRow()) - l.toggleBand(),
		W: pop.W - 2*gap,
		H: l.Cfg.ToggleH,
	}
}

// PointInToggle reports whether (x, y) is on the disclosure strip. False when
// there is no toggle, so a caller needs no second guard.
func (l Layout) PointInToggle(x, y float64) bool {
	if !l.Toggle {
		return false
	}
	r := l.ToggleRect()
	return x >= r.X && x <= r.X+r.W && y >= r.Y && y <= r.Y+r.H
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
