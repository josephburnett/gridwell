package textedit

import "testing"

func TestShouldDebouncedSaveFire(t *testing.T) {
	cases := []struct {
		name               string
		hasFocusedPane     bool
		textFocusTileID    string
		isTextMode         bool
		lastTextareaTileID string
		want               bool
	}{
		{"happy path: bound, text mode, focused", true, "7", true, "7", true},
		{"no focused pane", false, "7", true, "7", false},
		{"no text focus (grid pane)", true, "", true, "", false},
		{"rendered mode is read-only", true, "7", false, "7", false},
		{"textarea bound to a different tile (the regression)", true, "7", true, "9", false},
		{"stale: scheduled for A (last=5), now focused on B (7)", true, "7", true, "5", false},
		{"bound but no focus beats the tile match", false, "7", true, "7", false},
	}
	for _, c := range cases {
		got := ShouldDebouncedSaveFire(c.hasFocusedPane, c.textFocusTileID, c.isTextMode, c.lastTextareaTileID)
		if got != c.want {
			t.Errorf("%s: ShouldDebouncedSaveFire(%v,%q,%v,%q) = %v, want %v",
				c.name, c.hasFocusedPane, c.textFocusTileID, c.isTextMode, c.lastTextareaTileID, got, c.want)
		}
	}
}

func TestInsertAt(t *testing.T) {
	cases := []struct {
		src, ins string
		off      int
		want     string
		wantOff  int
	}{
		{"", "x", 0, "x", 1},
		{"ab", "X", 0, "Xab", 1},
		{"ab", "X", 1, "aXb", 2},
		{"ab", "X", 2, "abX", 3},
		{"ab", "X", 99, "abX", 3},  // clamp
		{"ab", "X", -1, "Xab", 1},  // clamp
		{"ab", "\n", 1, "a\nb", 2}, // newline (Enter)
	}
	for _, c := range cases {
		got, off := InsertAt(c.src, c.ins, c.off)
		if got != c.want || off != c.wantOff {
			t.Errorf("InsertAt(%q,%q,%d) = (%q,%d), want (%q,%d)", c.src, c.ins, c.off, got, off, c.want, c.wantOff)
		}
	}
}

func TestInsertParagraphBreak(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		off     int
		want    string
		wantOff int
	}{
		{"end of doc", "hello", 5, "hello\n\n", 7},
		{"mid word splits the paragraph", "hello", 2, "he\n\nllo", 4},
		{"between paragraphs is idempotent (caret at next para)", "a\n\nb", 3, "a\n\nb", 3},
		{"at end of para jumps past the break", "a\n\nb", 1, "a\n\nb", 3},
		{"inside the break normalizes in place", "a\n\nb", 2, "a\n\nb", 3},
		{"soft break upgrades to a paragraph break", "a\nb", 2, "a\n\nb", 3},
		{"extra blank lines collapse", "a\n\n\n\nb", 3, "a\n\nb", 3},
		{"trailing newlines collapse", "a\n\n\n", 4, "a\n\n", 3},
		{"empty doc", "", 0, "\n\n", 2},
		{"start of doc", "a", 0, "\n\na", 2},
		{"hard-break spaces are left alone", "a  \nb", 4, "a  \n\nb", 5},
		{"clamped past end", "a", 99, "a\n\n", 3},
		{"clamped before start", "a", -1, "\n\na", 2},
	}
	for _, c := range cases {
		got, off := InsertParagraphBreak(c.src, c.off)
		if got != c.want || off != c.wantOff {
			t.Errorf("%s: InsertParagraphBreak(%q,%d) = (%q,%d), want (%q,%d)",
				c.name, c.src, c.off, got, off, c.want, c.wantOff)
		}
	}
}

// TestInsertParagraphBreakIdempotent: pressing Enter repeatedly at the same
// boundary must converge — the source stops changing after the first press.
// This is the fix for the invisible-blank-line accumulation class.
func TestInsertParagraphBreakIdempotent(t *testing.T) {
	src, off := "one two", 3
	src, off = InsertParagraphBreak(src, off)
	for i := 0; i < 3; i++ {
		next, nextOff := InsertParagraphBreak(src, off)
		if next != src {
			t.Fatalf("press %d changed the source: %q -> %q", i+2, src, next)
		}
		src, off = next, nextOff
	}
	if src != "one\n\n two" {
		t.Errorf("converged source = %q, want %q", src, "one\n\n two")
	}
}

func TestDeleteBefore(t *testing.T) {
	cases := []struct {
		src     string
		off     int
		want    string
		wantOff int
	}{
		{"abc", 0, "abc", 0}, // nothing before
		{"abc", 1, "bc", 0},
		{"abc", 3, "ab", 2},
		{"café", 5, "caf", 3}, // é is 2 bytes: delete whole rune
		{"abc", 99, "ab", 2},  // clamp
	}
	for _, c := range cases {
		got, off := DeleteBefore(c.src, c.off)
		if got != c.want || off != c.wantOff {
			t.Errorf("DeleteBefore(%q,%d) = (%q,%d), want (%q,%d)", c.src, c.off, got, off, c.want, c.wantOff)
		}
	}
}

func TestDeleteAt(t *testing.T) {
	if got := DeleteAt("abc", 0); got != "bc" {
		t.Errorf("DeleteAt(abc,0) = %q", got)
	}
	if got := DeleteAt("café", 3); got != "caf" { // delete é (2 bytes)
		t.Errorf("DeleteAt(café,3) = %q", got)
	}
	if got := DeleteAt("abc", 3); got != "abc" { // at end: no-op
		t.Errorf("DeleteAt(abc,3) = %q", got)
	}
}

func TestMoveLeftRight(t *testing.T) {
	// "aé b": a=1 byte, é=2 bytes (offsets 1..3), space at 3, b at 4.
	s := "aé b"
	if MoveRight(s, 0) != 1 || MoveRight(s, 1) != 3 || MoveRight(s, 3) != 4 {
		t.Errorf("MoveRight chain wrong: %d %d %d", MoveRight(s, 0), MoveRight(s, 1), MoveRight(s, 3))
	}
	if MoveLeft(s, 3) != 1 || MoveLeft(s, 1) != 0 || MoveLeft(s, 0) != 0 {
		t.Errorf("MoveLeft chain wrong: %d %d %d", MoveLeft(s, 3), MoveLeft(s, 1), MoveLeft(s, 0))
	}
	if MoveRight(s, len(s)) != len(s) {
		t.Error("MoveRight past end should clamp")
	}
}
