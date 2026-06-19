package markdown

import (
	"strings"
	"testing"
)

func TestParseHeadings(t *testing.T) {
	bs := Parse("# H1\n## H2\n### H3\nplain")
	if len(bs) != 4 {
		t.Fatalf("blocks = %d", len(bs))
	}
	wants := []BlockKind{BlockHeading1, BlockHeading2, BlockHeading3, BlockParagraph}
	for i, b := range bs {
		if b.Kind != wants[i] {
			t.Errorf("block[%d] kind = %v, want %v", i, b.Kind, wants[i])
		}
	}
}

func TestParseListBlockquoteCodeFence(t *testing.T) {
	src := "- one\n* two\n> quote\n```\nx = 1\ny = 2\n```\n"
	bs := Parse(src)
	kinds := make([]BlockKind, len(bs))
	for i, b := range bs {
		kinds[i] = b.Kind
	}
	want := []BlockKind{BlockListItem, BlockListItem, BlockBlockquote, BlockCode, BlockBlank}
	if len(kinds) != len(want) {
		t.Fatalf("kinds = %v", kinds)
	}
	for i, k := range kinds {
		if k != want[i] {
			t.Errorf("block %d: kind = %v, want %v", i, k, want[i])
		}
	}
	codeBlock := bs[3]
	if !strings.Contains(codeBlock.Spans[0].Text, "x = 1") {
		t.Errorf("code block text = %q", codeBlock.Spans[0].Text)
	}
}

func TestParseInlineCode(t *testing.T) {
	bs := Parse("call `foo()` here")
	if len(bs) != 1 {
		t.Fatalf("blocks = %d", len(bs))
	}
	spans := bs[0].Spans
	if len(spans) != 3 {
		t.Fatalf("spans = %+v", spans)
	}
	if spans[1].Text != "foo()" || spans[1].Style != StyleCode {
		t.Errorf("middle span = %+v", spans[1])
	}
}

func TestParseInlineBoldItalic(t *testing.T) {
	bs := Parse("a **b** c *d* e _f_ g")
	spans := bs[0].Spans
	// Recombine to a stylized string so we can assert.
	got := ""
	for _, s := range spans {
		marker := ""
		if s.Style&StyleBold != 0 {
			marker += "B"
		}
		if s.Style&StyleItalic != 0 {
			marker += "I"
		}
		got += marker + ":" + s.Text + "|"
	}
	want := ":a |B:b|: c |I:d|: e |I:f|: g|"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestUnterminatedCodeFence(t *testing.T) {
	bs := Parse("```\nuh oh\n")
	if len(bs) != 1 || bs[0].Kind != BlockCode {
		t.Errorf("blocks = %+v", bs)
	}
}

func TestWrapHonorsMaxWidth(t *testing.T) {
	// One pixel per character.
	measure := func(sp Span) float64 { return float64(len(sp.Text)) }
	spans := []Span{{Text: "alpha bravo charlie delta echo foxtrot"}}
	lines := Wrap(spans, 12, measure)
	for i, l := range lines {
		w := 0.0
		for _, sp := range l {
			w += measure(sp)
		}
		if w > 12 {
			t.Errorf("line %d width %v > 12: %+v", i, w, l)
		}
	}
}

func TestWrapPreservesStyle(t *testing.T) {
	measure := func(sp Span) float64 { return float64(len(sp.Text)) }
	spans := []Span{
		{Text: "hello "},
		{Text: "WORLD", Style: StyleBold},
		{Text: " end"},
	}
	lines := Wrap(spans, 100, measure)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	hasBold := false
	for _, sp := range lines[0] {
		if sp.Style&StyleBold != 0 && sp.Text == "WORLD" {
			hasBold = true
		}
	}
	if !hasBold {
		t.Errorf("bold span lost: %+v", lines[0])
	}
}

func TestParseLink(t *testing.T) {
	bs := Parse("see [example](https://example.com) for more")
	if len(bs) != 1 {
		t.Fatalf("blocks = %d", len(bs))
	}
	spans := bs[0].Spans
	var link *Span
	for i := range spans {
		if spans[i].Style&StyleLink != 0 {
			link = &spans[i]
			break
		}
	}
	if link == nil {
		t.Fatalf("no link span: %+v", spans)
	}
	if link.Text != "example" {
		t.Errorf("link text = %q", link.Text)
	}
	if link.Href != "https://example.com" {
		t.Errorf("link href = %q", link.Href)
	}
}

func TestParseEmbed(t *testing.T) {
	src := "before [![alt](/preview/tile/5?w=192&h=128)](/3/4/5) after"
	bs := Parse(src)
	if len(bs) != 1 {
		t.Fatalf("blocks = %d", len(bs))
	}
	var embed *Span
	for i := range bs[0].Spans {
		if bs[0].Spans[i].Style&StyleEmbed != 0 {
			embed = &bs[0].Spans[i]
			break
		}
	}
	if embed == nil {
		t.Fatalf("no embed span: %+v", bs[0].Spans)
	}
	if embed.Alt != "alt" {
		t.Errorf("alt = %q", embed.Alt)
	}
	if embed.Src != "/preview/tile/5?w=192&h=128" {
		t.Errorf("src = %q", embed.Src)
	}
	if embed.Href != "/3/4/5" {
		t.Errorf("href = %q", embed.Href)
	}
	if embed.W != 192 || embed.H != 128 {
		t.Errorf("size = %dx%d, want 192x128", embed.W, embed.H)
	}
}

func TestParseImageAloneAsEmbed(t *testing.T) {
	bs := Parse("![pic](/img.png)")
	spans := bs[0].Spans
	if len(spans) != 1 || spans[0].Style&StyleEmbed == 0 {
		t.Fatalf("expected one embed span: %+v", spans)
	}
	if spans[0].Href != "" {
		t.Errorf("expected empty href for bare image, got %q", spans[0].Href)
	}
	if spans[0].Src != "/img.png" {
		t.Errorf("src = %q", spans[0].Src)
	}
}

func TestParseLinkPreservedAroundEmphasis(t *testing.T) {
	bs := Parse("**bold** [link](/x) *italic*")
	var hasLink, hasBold, hasItalic bool
	for _, sp := range bs[0].Spans {
		if sp.Style&StyleLink != 0 && sp.Text == "link" && sp.Href == "/x" {
			hasLink = true
		}
		if sp.Style&StyleBold != 0 && sp.Text == "bold" {
			hasBold = true
		}
		if sp.Style&StyleItalic != 0 && sp.Text == "italic" {
			hasItalic = true
		}
	}
	if !(hasLink && hasBold && hasItalic) {
		t.Errorf("missing one of link/bold/italic: %+v", bs[0].Spans)
	}
}

func TestParseMalformedLinkPassesThrough(t *testing.T) {
	bs := Parse("look at [this and then nothing")
	for _, sp := range bs[0].Spans {
		if sp.Style&StyleLink != 0 {
			t.Errorf("unexpected link span: %+v", sp)
		}
	}
}

// These exercise the error paths of the link / image / embed parsers
// so an unterminated bracket doesn't crash and doesn't pretend to have
// found an embed. The parser is intentionally lenient — when it finds
// link-shaped text-in-brackets, it'll classify as a link — so the
// strict invariant we want to pin is just "no embed span emerges from
// shapes that lack both the bracket-paren-bracket-paren run."
func TestParseEmbedRequiresAllTokens(t *testing.T) {
	cases := []string{
		"[!incomplete",
		"[![alt](no-href",
		"[![",
		"![bare image with no close",
		"![alt](unclosed",
		"![alt no bracket close",
		"[link no close",
		"[link](unclosed",
		"[link]missing-paren",
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			bs := Parse(c)
			for _, b := range bs {
				for _, sp := range b.Spans {
					if sp.Style&StyleEmbed != 0 {
						t.Errorf("malformed input produced embed span: %+v", sp)
					}
				}
			}
		})
	}
}

func TestEmbedSizeFromSrcEdges(t *testing.T) {
	cases := []struct {
		src   string
		w, h  int
	}{
		{"/preview/tile/5", 0, 0},          // no query
		{"/preview/tile/5?w=192&h=128", 192, 128},
		{"/preview/tile/5?w=0&h=0", 0, 0},     // explicit zero
		{"/preview/tile/5?h=128", 0, 128},      // only h
		{"/preview/tile/5?w=192", 192, 0},      // only w
		{"/preview/tile/5?w=abc&h=128", 0, 128}, // non-numeric → 0
		{"/preview/tile/5?w=&h=", 0, 0},          // empty values
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
		{"first blank then paragraph", "\n\nfirst real line\nsecond line", "first real line"},
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
		{"heading wins over later non-blank", "# H\n\nlater", "H"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AltFromSource(tc.src); got != tc.want {
				t.Errorf("AltFromSource(%q) = %q, want %q", tc.src, got, tc.want)
			}
		})
	}
}

func TestWrapLinksAtomic(t *testing.T) {
	// Plain markdown links should never split mid-link, even when the
	// text is wide enough to wrap. Treats them the same as embeds.
	spans := []Span{
		{Text: "see ", Style: StyleNone},
		{Text: "the first heading", Style: StyleLink, Href: "http://localhost:8080/5"},
		{Text: " end", Style: StyleNone},
	}
	measure := func(sp Span) float64 { return float64(len(sp.Text)) }
	lines := Wrap(spans, 10, measure) // forces wrap
	for _, line := range lines {
		for _, sp := range line {
			if sp.Style&StyleLink != 0 && sp.Text != "the first heading" {
				t.Errorf("link text got sliced: %+v", sp)
			}
		}
	}
}

func TestWrapEmbedsAtomicNotSplit(t *testing.T) {
	// Embed spans should never split mid-token. Confirm Wrap keeps an
	// embed atomic even when measure returns a width larger than the
	// available line.
	spans := []Span{
		{Text: "before ", Style: StyleNone},
		{Style: StyleEmbed, Src: "/preview/tile/5?w=200&h=80",
			Href: "/5", W: 200, H: 80},
		{Text: " after", Style: StyleNone},
	}
	measure := func(sp Span) float64 {
		if sp.Style&StyleEmbed != 0 {
			return float64(sp.W)
		}
		return float64(len(sp.Text))
	}
	lines := Wrap(spans, 50, measure) // forces wrapping
	embedCount := 0
	for _, line := range lines {
		for _, sp := range line {
			if sp.Style&StyleEmbed != 0 {
				embedCount++
				if sp.W != 200 || sp.H != 80 || sp.Href != "/5" {
					t.Errorf("embed span lost data after wrap: %+v", sp)
				}
			}
		}
	}
	if embedCount != 1 {
		t.Errorf("expected exactly one embed span across lines, got %d", embedCount)
	}
}

func TestWrapNoEarlyBreakOnSpaces(t *testing.T) {
	measure := func(sp Span) float64 { return float64(len(sp.Text)) }
	// "abc def" with width 7 should fit on one line.
	lines := Wrap([]Span{{Text: "abc def"}}, 7, measure)
	if len(lines) != 1 {
		t.Errorf("got %d lines: %+v", len(lines), lines)
	}
}

// TestParseInlineIntrawordUnderscore: a "_" between word characters is literal
// (snake_case must not render "case" italic), while boundary underscores and
// asterisks still emphasize.
func TestParseInlineIntrawordUnderscore(t *testing.T) {
	stylize := func(src string) string {
		spans := Parse(src)[0].Spans
		out := ""
		for _, s := range spans {
			m := ""
			if s.Style&StyleItalic != 0 {
				m = "I"
			}
			out += m + ":" + s.Text + "|"
		}
		return out
	}
	cases := map[string]string{
		"snake_case_var": ":snake_case_var|",        // all literal, no emphasis
		"_f_":            "I:f|",                     // boundary underscores emphasize
		"a _x_ b":        ":a |I:x|: b|",             // word-boundary emphasis still works
		"call foo_bar()": ":call foo_bar()|",         // identifier stays literal
	}
	for src, want := range cases {
		t.Run(src, func(t *testing.T) {
			if got := stylize(src); got != want {
				t.Errorf("stylize(%q) = %q, want %q", src, got, want)
			}
		})
	}
}
