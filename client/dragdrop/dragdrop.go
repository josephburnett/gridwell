// Package dragdrop holds the pure-math helpers used by the canvas client to
// translate cursor positions into grid cell coordinates and to validate
// proposed drops.
package dragdrop

// Pane describes the screen rectangle and viewport state for one pane. CellPx
// is the rendered size of one cell at zoom 1.0; the actual pixel size on
// screen is CellPx*Zoom.
type Pane struct {
	ScreenX, ScreenY float64 // top-left of the pane in screen coordinates
	ScreenW, ScreenH float64
	Cx, Cy           float64 // viewport center in cells
	Zoom             float64
	CellPx           float64
}

// ScreenToCell converts (sx, sy) in screen coordinates to (cx, cy) in cell
// coordinates within the pane's viewport. Returns floating-point cells; the
// caller floors / rounds depending on context.
func (p Pane) ScreenToCell(sx, sy float64) (float64, float64) {
	// Top-left of the viewport in screen space is centered on (Cx, Cy).
	// Scale: 1 cell = CellPx * Zoom screen pixels.
	cellSize := p.CellPx * p.Zoom
	cx := p.Cx + (sx-(p.ScreenX+p.ScreenW/2))/cellSize
	cy := p.Cy + (sy-(p.ScreenY+p.ScreenH/2))/cellSize
	return cx, cy
}

// CellToScreen does the inverse mapping.
func (p Pane) CellToScreen(cx, cy float64) (float64, float64) {
	cellSize := p.CellPx * p.Zoom
	sx := p.ScreenX + p.ScreenW/2 + (cx-p.Cx)*cellSize
	sy := p.ScreenY + p.ScreenH/2 + (cy-p.Cy)*cellSize
	return sx, sy
}

// PaneAt returns the pane (by index) under (sx, sy) given an ordered slice
// of pane rectangles. Returns -1 if no pane covers the point. Panes are
// expected to be axis-aligned and non-overlapping.
func PaneAt(panes []Pane, sx, sy float64) int {
	for i, p := range panes {
		if sx >= p.ScreenX && sy >= p.ScreenY &&
			sx < p.ScreenX+p.ScreenW && sy < p.ScreenY+p.ScreenH {
			return i
		}
	}
	return -1
}

// SnapToCell rounds a floating-cell coordinate to the nearest whole cell.
// We round-half-down (floor + 0.5) so a drag exactly on the boundary lands
// in the lower-numbered cell. This matches typical UI expectations where
// dragging onto the leading edge "stays" rather than "advances".
func SnapToCell(c float64) int64 {
	if c >= 0 {
		return int64(c + 0.5)
	}
	// For negative values, biasing toward zero keeps the boundary
	// behavior symmetric.
	return int64(c - 0.5)
}

// FootprintFits reports whether a (w, h)-sized footprint at (x, y) fits
// inside the pane's framed rectangle. The framed rect is computed from the
// pane's screen size, zoom, and viewport center. Used by the client to gray
// out drop targets before shipping any RPC.
func (p Pane) FootprintFits(x, y, w, h int64) bool {
	cellSize := p.CellPx * p.Zoom
	visibleW := p.ScreenW / cellSize
	visibleH := p.ScreenH / cellSize
	left := p.Cx - visibleW/2
	right := p.Cx + visibleW/2
	top := p.Cy - visibleH/2
	bottom := p.Cy + visibleH/2
	return float64(x) >= left && float64(x+w) <= right &&
		float64(y) >= top && float64(y+h) <= bottom
}
