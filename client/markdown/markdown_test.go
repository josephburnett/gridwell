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

func TestWrapNoEarlyBreakOnSpaces(t *testing.T) {
	measure := func(sp Span) float64 { return float64(len(sp.Text)) }
	// "abc def" with width 7 should fit on one line.
	lines := Wrap([]Span{{Text: "abc def"}}, 7, measure)
	if len(lines) != 1 {
		t.Errorf("got %d lines: %+v", len(lines), lines)
	}
}
