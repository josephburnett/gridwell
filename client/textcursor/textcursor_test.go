package textcursor

import "testing"

func TestOffsetFromRowCol(t *testing.T) {
	const src = "ab\ncde\n\nfg"
	// offsets:    0123 4567 8 9 10
	//   row 0 "ab"  -> 0..2
	//   row 1 "cde" -> 3..6
	//   row 2 ""    -> 7
	//   row 3 "fg"  -> 8..10
	cases := []struct {
		name     string
		src      string
		row, col int
		want     int
	}{
		{"start", src, 0, 0, 0},
		{"mid first line", src, 0, 1, 1},
		{"end first line", src, 0, 2, 2},
		{"col past line clamps to line end", src, 0, 99, 2},
		{"second line start", src, 1, 0, 3},
		{"second line end", src, 1, 3, 6},
		{"empty line", src, 2, 0, 7},
		{"empty line col clamps", src, 2, 5, 7},
		{"last line", src, 3, 0, 8},
		{"last line end", src, 3, 2, 10},
		{"row past end returns len", src, 9, 0, len(src)},
		{"negative row", src, -1, 0, 0},
		{"negative col", src, 0, -5, 0},
		{"empty source", "", 0, 0, 0},
		{"empty source row past", "", 3, 3, 0},
		{"crlf keeps cr in line", "a\r\nb", 0, 99, 2}, // "a\r" is line 0, len 2
		{"crlf second line", "a\r\nb", 1, 0, 3},
	}
	for _, c := range cases {
		if got := OffsetFromRowCol(c.src, c.row, c.col); got != c.want {
			t.Errorf("%s: OffsetFromRowCol(%q,%d,%d)=%d want %d", c.name, c.src, c.row, c.col, got, c.want)
		}
	}
}

func TestRowColFromOffset(t *testing.T) {
	const src = "ab\ncde\n\nfg"
	cases := []struct {
		name      string
		src       string
		off       int
		wRow, wCol int
	}{
		{"start", src, 0, 0, 0},
		{"first line", src, 1, 0, 1},
		{"after first newline", src, 3, 1, 0},
		{"second line mid", src, 5, 1, 2},
		{"empty line", src, 7, 2, 0},
		{"last line", src, 9, 3, 1},
		{"end", src, 10, 3, 2},
		{"offset past end clamps", src, 999, 3, 2},
		{"negative clamps to start", src, -4, 0, 0},
		{"empty source", "", 0, 0, 0},
		{"crlf cr counts as col", "a\r\nb", 2, 0, 2},
	}
	for _, c := range cases {
		row, col := RowColFromOffset(c.src, c.off)
		if row != c.wRow || col != c.wCol {
			t.Errorf("%s: RowColFromOffset(%q,%d)=(%d,%d) want (%d,%d)", c.name, c.src, c.off, row, col, c.wRow, c.wCol)
		}
	}
}

// TestRoundTrip: for offsets that land at a real character position,
// OffsetFromRowCol(RowColFromOffset) is the identity.
func TestRoundTrip(t *testing.T) {
	srcs := []string{"", "x", "ab\ncde\n\nfg", "a\r\nb\r\nc", "\n\n\n", "no newline at all"}
	for _, src := range srcs {
		for off := 0; off <= len(src); off++ {
			row, col := RowColFromOffset(src, off)
			if got := OffsetFromRowCol(src, row, col); got != off {
				t.Errorf("round trip %q off=%d -> (%d,%d) -> %d", src, off, row, col, got)
			}
		}
	}
}
