package store

import (
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// nextFreeRect is the one first-free-cell rule; the overlay and the trash fill
// both read it. The reading order wraps at FreeSlotWidth, a slot skips
// cells any of which are taken, and a slot wider than the fill still lands.
func TestNextFreeRectIsTheOneReadingOrder(t *testing.T) {
	occ := map[[2]int64]bool{}
	var cur cursor
	for i := int64(0); i < FreeSlotWidth+1; i++ {
		x, y := nextFreeRect(occ, &cur, 1, 1)
		wantX, wantY := i%FreeSlotWidth, i/FreeSlotWidth
		if x != wantX || y != wantY {
			t.Fatalf("slot %d = (%d,%d), want (%d,%d)", i, x, y, wantX, wantY)
		}
	}

	// A 2x1 slot skips two full rows and lands on row 2, where the user's own
	// tile at (0,2) is flowed around. The rows are taken past the width too,
	// because a slot's origin wraps there but its cells do not.
	occ = map[[2]int64]bool{}
	occupyRect(occ, 0, 0, FreeSlotWidth+1, 1)
	occupyRect(occ, 0, 1, FreeSlotWidth+1, 1)
	occupyRect(occ, 0, 2, 1, 1)
	cur = cursor{}
	if x, y := nextFreeRect(occ, &cur, 2, 1); x != 1 || y != 2 {
		t.Fatalf("2x1 slot = (%d,%d), want (1,2)", x, y)
	}
	// The cells are taken now, so the next 1x1 lands past them.
	if x, y := nextFreeRect(occ, &cur, 1, 1); x != 3 || y != 2 {
		t.Fatalf("next 1x1 after the 2x1 = (%d,%d), want (3,2)", x, y)
	}

	// Wider than the fill: it lands at the row start and runs past the width.
	occ = map[[2]int64]bool{}
	cur = cursor{}
	if x, y := nextFreeRect(occ, &cur, FreeSlotWidth+4, 1); x != 0 || y != 0 {
		t.Fatalf("wide slot = (%d,%d), want (0,0)", x, y)
	}
	if !occ[[2]int64{FreeSlotWidth + 3, 0}] {
		t.Fatal("the wide slot's far cells are not marked occupied")
	}
}

// FreeSlot reads the same rule over a grid's rows: with no cell named it is
// the reading order from the origin, and a named cell is a hint that stacks
// below in its own column when taken, as a plugin's hint does.
func TestFreeSlotIsTheFirstFreeCellOverAGridsRows(t *testing.T) {
	taken := []*gridwellv1.Tile{{X: 0, Y: 0, W: 1, H: 1}, {X: 1, Y: 0, W: 2, H: 1}, {X: 5, Y: 3, W: 1, H: 2}}
	if x, y := FreeSlot(taken, nil, 1, 1); x != 3 || y != 0 {
		t.Fatalf("unaimed = (%d,%d), want (3,0)", x, y)
	}
	if x, y := FreeSlot(taken, &[2]int64{5, 3}, 1, 1); x != 5 || y != 5 {
		t.Fatalf("near a taken cell = (%d,%d), want (5,5), below it in its column", x, y)
	}
	if x, y := FreeSlot(taken, &[2]int64{9, 9}, 1, 1); x != 9 || y != 9 {
		t.Fatalf("near a free cell = (%d,%d), want the cell itself", x, y)
	}
}
