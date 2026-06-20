package markdown

import (
	"strings"
	"testing"
)

func TestEmbedSizeFromSrcEdges(t *testing.T) {
	cases := []struct {
		src  string
		w, h int
	}{
		{"/preview/tile/5", 0, 0}, // no query
		{"/preview/tile/5?w=192&h=128", 192, 128},
		{"/preview/tile/5?w=0&h=0", 0, 0},       // explicit zero
		{"/preview/tile/5?h=128", 0, 128},       // only h
		{"/preview/tile/5?w=192", 192, 0},       // only w
		{"/preview/tile/5?w=abc&h=128", 0, 128}, // non-numeric → 0
		{"/preview/tile/5?w=&h=", 0, 0},         // empty values
		{"/preview/tile/5?other=stuff", 0, 0},   // ignored params
	}
	for _, tc := range cases {
		t.Run(tc.src, func(t *testing.T) {
			gotW, gotH := embedSizeFromSrc(tc.src)
			if gotW != tc.w || gotH != tc.h {
				t.Errorf("embedSizeFromSrc(%q) = %d,%d; want %d,%d",
					tc.src, gotW, gotH, tc.w, tc.h)
			}
		})
	}
}

func TestAltFromSource(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"empty source", "", ""},
		{"only blanks", "\n\n\n", ""},
		{"plain paragraph", "hello world", "hello world"},
		{"H1 heading", "# Heading One\n\nbody", "Heading One"},
		{"H2 heading", "## Sub", "Sub"},
		// goldmark joins the two physical lines into one paragraph (soft break
		// → space), so the alt now summarizes the whole first paragraph.
		{"soft-wrapped first paragraph", "\n\nfirst real line\nsecond line", "first real line second line"},
		{"bold and italic stripped", "**bold** and *italic*", "bold and italic"},
		{"inline code retained", "use `foo()` here", "use foo() here"},
		{"link text retained", "click [here](https://x) please", "click here please"},
		{"embed skipped, surrounding text kept", "before [![alt](src)](href) after", "before after"},
		// A code-block-first doc must not leak its newlines into the alt — a
		// newline would break the generated [alt](href) embed link.
		{"code block collapses to one line", "```\nfoo\nbar\n```", "foo bar"},
		{"internal whitespace collapsed", "a    b\tc", "a b c"},
		{"clamped to 100 runes",
			"x" + strings.Repeat("y", 200),
			"x" + strings.Repeat("y", 99)},
		{"heading wins over later block", "# H\n\nlater", "H"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AltFromSource(tc.src); got != tc.want {
				t.Errorf("AltFromSource(%q) = %q, want %q", tc.src, got, tc.want)
			}
		})
	}
}
