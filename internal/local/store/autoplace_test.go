package store

import "testing"

// nextFreeRect is the one auto-place rule; the overlay and the trash fill
// both read it. The reading order wraps at AutoPlaceWidth, a slot skips
// cells any of which are taken, and a slot wider than the fill still lands.
func TestNextFreeRectIsTheOneReadingOrder(t *testing.T) {
	occ := map[[2]int64]bool{}
	var cur cursor
	for i := int64(0); i < AutoPlaceWidth+1; i++ {
		x, y := nextFreeRect(occ, &cur, 1, 1)
		wantX, wantY := i%AutoPlaceWidth, i/AutoPlaceWidth
		if x != wantX || y != wantY {
			t.Fatalf("slot %d = (%d,%d), want (%d,%d)", i, x, y, wantX, wantY)
		}
	}

	// A 2x1 slot skips two full rows and lands on row 2, where the user's own
	// tile at (0,2) is flowed around. The rows are taken past the width too,
	// because a slot's origin wraps there but its cells do not.
	occ = map[[2]int64]bool{}
	occupyRect(occ, 0, 0, AutoPlaceWidth+1, 1)
	occupyRect(occ, 0, 1, AutoPlaceWidth+1, 1)
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
	if x, y := nextFreeRect(occ, &cur, AutoPlaceWidth+4, 1); x != 0 || y != 0 {
		t.Fatalf("wide slot = (%d,%d), want (0,0)", x, y)
	}
	if !occ[[2]int64{AutoPlaceWidth + 3, 0}] {
		t.Fatal("the wide slot's far cells are not marked occupied")
	}
}
