package textedit

import "testing"

func TestShouldDebouncedSaveFire(t *testing.T) {
	cases := []struct {
		name               string
		hasFocusedPane     bool
		textFocusTileID    int64
		isTextMode         bool
		lastTextareaTileID int64
		want               bool
	}{
		{"happy path: bound, text mode, focused", true, 7, true, 7, true},
		{"no focused pane", false, 7, true, 7, false},
		{"no text focus (grid pane)", true, 0, true, 0, false},
		{"rendered mode is read-only", true, 7, false, 7, false},
		{"textarea bound to a different tile (the regression)", true, 7, true, 9, false},
		{"stale: scheduled for A (last=5), now focused on B (7)", true, 7, true, 5, false},
		{"bound but no focus beats the tile match", false, 7, true, 7, false},
	}
	for _, c := range cases {
		got := ShouldDebouncedSaveFire(c.hasFocusedPane, c.textFocusTileID, c.isTextMode, c.lastTextareaTileID)
		if got != c.want {
			t.Errorf("%s: ShouldDebouncedSaveFire(%v,%d,%v,%d) = %v, want %v",
				c.name, c.hasFocusedPane, c.textFocusTileID, c.isTextMode, c.lastTextareaTileID, got, c.want)
		}
	}
}
