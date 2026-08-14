package griddb

// NextEmptyCell returns the first unoccupied (x, y) cell in row-major order,
// wrapping to the next row at `width`, and marks it occupied in the map so a
// sequence of calls fills the grid left-to-right, top-to-bottom without
// collisions. It is the auto-layout the fs and proc plugins use to place a
// freshly-discovered file/process tile: existing tiles keep their stored
// position (their cells are pre-seeded into `occupied`), and only the new
// arrivals are laid into the gaps — so a directory listing a user has
// rearranged stays rearranged, and a new file lands in the first free slot.
//
// width must be >= 1. The scan always terminates: each step either returns a
// free cell or advances, and rows are unbounded, so a free cell always exists.
func NextEmptyCell(occupied map[[2]int64]bool, width int64) (int64, int64) {
	var cx, cy int64
	for {
		if !occupied[[2]int64{cx, cy}] {
			occupied[[2]int64{cx, cy}] = true
			return cx, cy
		}
		cx++
		if cx >= width {
			cx = 0
			cy++
		}
	}
}

// OccupyRect marks a tile's FULL w×h footprint occupied — the same rule
// the localdb's overlap check enforces (store/place.go). Seeding only the
// origin cell (the old fs/proc habit) left a resized tile's interior
// "free", so reconcile dropped newly discovered entries INSIDE existing
// tiles.
func OccupyRect(occupied map[[2]int64]bool, x, y, w, h int64) {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	for dx := int64(0); dx < w; dx++ {
		for dy := int64(0); dy < h; dy++ {
			occupied[[2]int64{x + dx, y + dy}] = true
		}
	}
}
