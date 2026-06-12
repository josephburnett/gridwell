// Package palette computes the layout of Gridwell's tile-creation
// palette — the popover that opens over the pane's "+" button and
// holds the four template tile previews (well, markdown, url,
// blackhole) that the user drags onto the canvas.
//
// All layout is pure: it depends only on the pane's screen rect, the
// pane's current zoom (so the previews track the size of the placed
// tile), and the number of template tiles. The wasm renderer reads
// the rects out and paints into them.
package palette

// Rect is a screen-space rectangle. Mirrors pane.Rect / dragdrop screen
// rectangles; kept here so the package has no cross-imports.
type Rect struct {
	X, Y, W, H float64
}

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
		CellPx:     64,
	}
}

// Layout snapshots one palette's input. All methods are pure and
// don't allocate.
type Layout struct {
	Cfg      Config
	Pane     Rect
	PaneZoom float64
	NumTiles int
}

// PlusCenter returns the screen-space center of the + button.
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
	return l.Cfg.CellPx * 0.5
}

// PopoverRect returns the screen rect of the entire palette popover,
// anchored just above the + button.
func (l Layout) PopoverRect() Rect {
	tile := l.TilePx()
	w := float64(l.NumTiles)*tile + float64(l.NumTiles+1)*l.Cfg.GapPx
	h := tile + 2*l.Cfg.GapPx
	cx, cy := l.PlusCenter()
	x := cx + l.Cfg.PlusRadius - w
	y := cy - l.Cfg.PlusRadius - h - 8
	return Rect{X: x, Y: y, W: w, H: h}
}

// TileRect returns the screen rect of the i'th template tile inside
// the popover.
func (l Layout) TileRect(i int) Rect {
	pop := l.PopoverRect()
	tile := l.TilePx()
	return Rect{
		X: pop.X + l.Cfg.GapPx + float64(i)*(tile+l.Cfg.GapPx),
		Y: pop.Y + l.Cfg.GapPx,
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
