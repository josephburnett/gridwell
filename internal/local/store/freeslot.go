package store

// The one automatic placement: where a tile lands when nobody chose a cell. A
// plugin entry with no placement hint takes the next free slot of its
// collection's overlay, a deleted tile takes the next free slot of its trash
// month, and a tile the MCP API creates or copies takes FreeSlot. All read
// this rule, so touching one cannot move another. It is layout only; the user
// rearranges afterwards and it stays as left.

import gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"

// FreeSlot is where a w×h tile lands among taken: at or below near in near's
// column when the caller names a cell, as a plugin's hint does, else the first
// free slot from the origin in reading order.
func FreeSlot(taken []*gridwellv1.Tile, near *[2]int64, w, h int64) (x, y int64) {
	occupied := map[[2]int64]bool{}
	for _, t := range taken {
		occupyRect(occupied, t.GetX(), t.GetY(), t.GetW(), t.GetH())
	}
	if near != nil {
		x, y = near[0], belowHint(occupied, near[0], near[1], w, h)
		return x, y
	}
	var cur cursor
	return nextFreeRect(occupied, &cur, w, h)
}

// belowHint is the first row at or below y where a w×h rect at column x is
// free, so a hint is a preference that never lands on an occupied cell.
func belowHint(occupied map[[2]int64]bool, x, y, w, h int64) int64 {
	for !rectFree(occupied, x, y, max(w, 1), max(h, 1)) {
		y++
	}
	return y
}

// FreeSlotWidth is the fill width the reading order wraps at.
const FreeSlotWidth int64 = 8

// cursor is where the next scan starts. A fresh one scans from the origin; a
// running one lets a listing flow without re-walking what it already placed.
type cursor struct{ x, y int64 }

// nextFreeRect is the first w×h slot, from cur in reading order, whose cells
// are all free. The slot's origin wraps at FreeSlotWidth; the slot itself may
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
		if cx >= FreeSlotWidth {
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

func vacateRect(occupied map[[2]int64]bool, x, y, w, h int64) {
	for dx := int64(0); dx < max(w, 1); dx++ {
		for dy := int64(0); dy < max(h, 1); dy++ {
			delete(occupied, [2]int64{x + dx, y + dy})
		}
	}
}
