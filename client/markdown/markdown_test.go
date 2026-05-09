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
	measure := func(text string, _ SpanStyle) float64 { return float64(len(text)) }
	spans := []Span{{Text: "alpha bravo charlie delta echo foxtrot"}}
	lines := Wrap(spans, 12, measure)
	for i, l := range lines {
		w := 0.0
		for _, sp := range l {
			w += measure(sp.Text, sp.Style)
		}
		if w > 12 {
			t.Errorf("line %d width %v > 12: %+v", i, w, l)
		}
	}
}

func TestWrapPreservesStyle(t *testing.T) {
	measure := func(text string, _ SpanStyle) float64 { return float64(len(text)) }
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

func TestWrapNoEarlyBreakOnSpaces(t *testing.T) {
	measure := func(text string, _ SpanStyle) float64 { return float64(len(text)) }
	// "abc def" with width 7 should fit on one line.
	lines := Wrap([]Span{{Text: "abc def"}}, 7, measure)
	if len(lines) != 1 {
		t.Errorf("got %d lines: %+v", len(lines), lines)
	}
}
