package store

// The one automatic placement: where a tile lands when nobody chose a cell. A
// plugin entry with no placement hint takes the next free slot of its
// collection's overlay, and a deleted tile takes the next free slot of its
// trash month. Both read this rule, so touching one cannot move the other.
// It is layout only; the user rearranges afterwards and it stays as left.

// AutoPlaceWidth is the fill width the reading order wraps at.
const AutoPlaceWidth int64 = 8

// cursor is where the next scan starts. A fresh one scans from the origin; a
// running one lets a listing flow without re-walking what it already placed.
type cursor struct{ x, y int64 }

// nextFreeRect is the first w×h slot, from cur in reading order, whose cells
// are all free. The slot's origin wraps at AutoPlaceWidth; the slot itself may
// run past it, because a tile wider than the fill still has to land. The cells
// are marked occupied and the cursor moved to the slot.
func nextFreeRect(occupied map[[2]int64]bool, cur *cursor, w, h int64) (x, y int64) {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	cx, cy := cur.x, cur.y
	for {
		if rectFree(occupied, cx, cy, w, h) {
			occupyRect(occupied, cx, cy, w, h)
			cur.x, cur.y = cx, cy
			return cx, cy
		}
		cx++
		if cx >= AutoPlaceWidth {
			cx = 0
			cy++
		}
	}
}

func rectFree(occupied map[[2]int64]bool, x, y, w, h int64) bool {
	for dx := int64(0); dx < w; dx++ {
		for dy := int64(0); dy < h; dy++ {
			if occupied[[2]int64{x + dx, y + dy}] {
				return false
			}
		}
	}
	return true
}

func occupyRect(occupied map[[2]int64]bool, x, y, w, h int64) {
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
