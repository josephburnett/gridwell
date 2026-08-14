package griddb

import "testing"

// TestNextEmptyCellFillsRowMajor: from an empty grid, successive calls fill
// left-to-right then wrap to the next row at `width`.
func TestNextEmptyCellFillsRowMajor(t *testing.T) {
	occupied := map[[2]int64]bool{}
	const width = 3
	want := [][2]int64{{0, 0}, {1, 0}, {2, 0}, {0, 1}, {1, 1}, {2, 1}, {0, 2}}
	for i, w := range want {
		x, y := NextEmptyCell(occupied, width)
		if x != w[0] || y != w[1] {
			t.Fatalf("call %d = (%d,%d), want (%d,%d)", i, x, y, w[0], w[1])
		}
	}
}

// TestNextEmptyCellSkipsOccupied: pre-seeded cells (existing tiles that kept
// their stored position) are stepped over; the new tile lands in the first gap.
func TestNextEmptyCellSkipsOccupied(t *testing.T) {
	occupied := map[[2]int64]bool{
		{0, 0}: true,
		{1, 0}: true,
		// (2,0) is the first free cell.
	}
	const width = 4
	if x, y := NextEmptyCell(occupied, width); x != 2 || y != 0 {
		t.Fatalf("first free = (%d,%d), want (2,0)", x, y)
	}
	// (3,0) free, then wrap to (0,1).
	if x, y := NextEmptyCell(occupied, width); x != 3 || y != 0 {
		t.Fatalf("second = (%d,%d), want (3,0)", x, y)
	}
	if x, y := NextEmptyCell(occupied, width); x != 0 || y != 1 {
		t.Fatalf("third = (%d,%d), want (0,1)", x, y)
	}
}

// TestNextEmptyCellMarksOccupied: the returned cell is marked, so it is never
// handed out twice — the property the reconcile loop relies on.
func TestNextEmptyCellMarksOccupied(t *testing.T) {
	occupied := map[[2]int64]bool{}
	seen := map[[2]int64]bool{}
	for i := 0; i < 50; i++ {
		x, y := NextEmptyCell(occupied, 8)
		if seen[[2]int64{x, y}] {
			t.Fatalf("cell (%d,%d) handed out twice", x, y)
		}
		seen[[2]int64{x, y}] = true
	}
}

// TestNextEmptyCellWidthOne degenerates to a single column.
func TestNextEmptyCellWidthOne(t *testing.T) {
	occupied := map[[2]int64]bool{}
	for i := int64(0); i < 4; i++ {
		x, y := NextEmptyCell(occupied, 1)
		if x != 0 || y != i {
			t.Fatalf("call %d = (%d,%d), want (0,%d)", i, x, y, i)
		}
	}
}

// A resized tile occupies its WHOLE footprint: auto-layout must never
// drop a new entry inside an existing 2x2 (the fs/proc reconcile bug —
// only the origin cell was seeded).
func TestOccupyRectFootprint(t *testing.T) {
	occ := map[[2]int64]bool{}
	OccupyRect(occ, 0, 0, 2, 2)
	x, y := NextEmptyCell(occ, 8)
	if x == 1 && y == 0 || x == 0 && y == 1 || x == 1 && y == 1 {
		t.Fatalf("NextEmptyCell landed inside the 2x2 footprint: (%d,%d)", x, y)
	}
	if x != 2 || y != 0 {
		t.Errorf("NextEmptyCell = (%d,%d), want (2,0)", x, y)
	}
	// Degenerate sizes count as 1x1.
	occ2 := map[[2]int64]bool{}
	OccupyRect(occ2, 3, 3, 0, 0)
	if !occ2[[2]int64{3, 3}] || len(occ2) != 1 {
		t.Errorf("degenerate footprint = %v", occ2)
	}
}
