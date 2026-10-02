package textcursor

import "testing"

// A press is mapped through the same soft wrap the painter draws, so a click
// on a wrapped row lands in that row's slice of its source line.
func TestCaretAt(t *testing.T) {
	// 10px per column and per row, from the origin.
	g := Grid{Cols: 8, Slot: 10, Adv: 10}
	at := func(col, row float64) (float64, float64) { return col*10 + 1, row*10 + 5 }
	const src = "line one\nhello brave world\n\nend"
	// Painted rows at 8 columns:
	//   0 "line one"   offsets 0..8
	//   1 "hello "     9..15
	//   2 "brave "     15..21
	//   3 "world"      21..26
	//   4 ""           27
	//   5 "end"        28..31
	cases := []struct {
		name     string
		col, row float64
		want     int
	}{
		{"start", 0, 0, 0},
		{"mid first row", 5, 0, 5},
		{"past a row's end clamps to it", 20, 0, 8},
		{"a soft-wrapped row starts mid-line", 0, 2, 15},
		{"inside a soft-wrapped row", 2, 2, 17},
		{"third slice of the line", 3, 3, 24},
		{"empty line", 4, 4, 27},
		{"last row", 1, 5, 29},
		{"below the last row is the end", 0, 30, len(src)},
		{"left of the text clamps to the row start", -3, 1, 9},
		{"above the text clamps to the first row", 2, -4, 2},
	}
	for _, c := range cases {
		x, y := at(c.col, c.row)
		if got := CaretAt(src, g, x, y); got != c.want {
			t.Errorf("%s: CaretAt(col %v, row %v) = %d, want %d", c.name, c.col, c.row, got, c.want)
		}
	}
}

// The textarea counts UTF-16 code units, so a character outside the BMP is
// two of them.
func TestCaretAtCountsUTF16(t *testing.T) {
	g := Grid{Cols: 0, Slot: 10, Adv: 10}
	if got := CaretAt("a😀b\nc", g, 21, 5); got != 3 {
		t.Errorf("after the emoji = %d, want 3", got)
	}
	if got := CaretAt("a😀b\nc", g, 11, 15); got != 6 {
		t.Errorf("second line end = %d, want 6", got)
	}
}

// The grid's origin and margins shift every press by the same amount.
func TestCaretAtHonorsTheOrigin(t *testing.T) {
	g := Grid{Cols: 0, Left: 6, Top: 6, Slot: 10, Adv: 10}
	if got := CaretAt("abc\ndef", g, 6+20, 6+15); got != 6 {
		t.Errorf("= %d, want 6", got)
	}
}
