package markdown

import (
	"reflect"
	"strings"
	"testing"
)

// WrapRawLine reproduces a monospace textarea's soft wrap, so the canvas
// painter and the editor agree row for row and the text never reflows when
// focus moves.
func TestWrapRawLine(t *testing.T) {
	cases := []struct {
		name string
		line string
		cols int
		want []string
	}{
		{"fits", "aa bb", 5, []string{"aa bb"}},
		{"empty", "", 5, []string{""}},
		{"no wrap when cols unset", "aa bb cc", 0, []string{"aa bb cc"}},
		{"breaks before the word that would overflow", "aa bb", 4, []string{"aa ", "bb"}},
		{"straddling word moves down whole", "aa bb cc", 7, []string{"aa bb ", "cc"}},
		{"spaces hang past the edge", "aaa bb", 3, []string{"aaa ", "bb"}},
		{"multi-space run hangs whole", "aa   bbb", 4, []string{"aa   ", "bbb"}},
		{"long word char-breaks at the limit", "abcdefgh", 3, []string{"abc", "def", "gh"}},
		{"long word breaks only once on its own row", "aa bcdefg", 5, []string{"aa ", "bcdef", "g"}},
		{"trailing spaces at end of line stay", "aa   ", 3, []string{"aa   "}},
		{"multiple wraps", "one two three four", 8, []string{"one two ", "three ", "four"}},
		{"runes count as columns", "ää öö üü", 4, []string{"ää ", "öö ", "üü"}},
	}
	for _, c := range cases {
		if got := WrapRawLine(c.line, c.cols); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: WrapRawLine(%q, %d) = %q, want %q", c.name, c.line, c.cols, got, c.want)
		}
	}
}

// Rejoining the rows reproduces the source line, because wrapping only
// chooses break points.
func TestWrapRawLineLossless(t *testing.T) {
	lines := []string{
		"the quick brown fox jumps over the lazy dog",
		"x  y   z    w",
		"supercalifragilisticexpialidocious",
		"   leading spaces stay",
	}
	for _, ln := range lines {
		for cols := 1; cols < 12; cols++ {
			joined := ""
			for _, row := range WrapRawLine(ln, cols) {
				joined += row
			}
			if joined != ln {
				t.Fatalf("cols %d: rows lose content: %q -> %q", cols, ln, joined)
			}
		}
	}
}

func TestWrapRawText(t *testing.T) {
	got := WrapRawText("aa bb\n\ncc", 4, 0)
	want := []string{"aa ", "bb", "", "cc"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("WrapRawText = %q, want %q", got, want)
	}
}

// A bounded wrap is the whole wrap's first rows, and reads no further: a
// row past the bound is never wrapped, whatever the document's length.
func TestWrapRawTextStopsAtTheRowsItShows(t *testing.T) {
	src := "aa bb\n\ncc\ndd"
	whole := WrapRawText(src, 4, 0)
	for rows := 1; rows <= len(whole); rows++ {
		got := WrapRawText(src, 4, rows)
		if len(got) < rows || !reflect.DeepEqual(got, whole[:len(got)]) {
			t.Fatalf("rows %d: %q, want a prefix of %q at least %d long", rows, got, whole, rows)
		}
	}
	if got := WrapRawText("aa bb\n"+strings.Repeat("x\n", 1<<20), 4, 2); len(got) != 2 {
		t.Fatalf("a megabyte under a two-row bound wrapped %d rows", len(got))
	}
}
