package doctype

import (
	"strings"
	"testing"
)

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
		// goldmark joins the two physical lines into one paragraph, turning
		// the soft break into a space, so the alt summarizes the whole first
		// paragraph.
		{"soft-wrapped first paragraph", "\n\nfirst real line\nsecond line", "first real line second line"},
		{"bold and italic stripped", "**bold** and *italic*", "bold and italic"},
		{"inline code retained", "use `foo()` here", "use foo() here"},
		{"link text retained", "click [here](https://x) please", "click here please"},
		{"embed skipped, surrounding text kept", "before [![alt](src)](href) after", "before after"},
		// A code-block-first document must not leak its newlines into the
		// alt: the alt is a single line by contract.
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
