package embed_test

import (
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/client/embed"
	"github.com/josephburnett/gridwell/client/markdown"
)

const testOrigin = "http://localhost:8080"

// TestDropRoundTripsThroughParser exercises the full chain a real drop
// follows: build the markdown, insert into doc source, parse back, and
// assert that the link href resolves to the right tile id. The plain
// link form means the parser produces a StyleLink span rather than a
// StyleEmbed; the rendered-mode renderer is what paints it as a
// preview by intercepting the href pattern.
func TestDropRoundTripsThroughParser(t *testing.T) {
	doc := "first line\nsecond line\n"
	link := embed.Markdown(testOrigin, 5, embed.DefaultAlt("text", 5))

	off := embed.LineEndOffset(doc, 0)
	out := embed.Insert(doc, link, off)

	blocks := markdown.Parse(out)
	var linkSpan *markdown.Span
	for _, b := range blocks {
		for i := range b.Spans {
			if b.Spans[i].Style&markdown.StyleLink != 0 {
				linkSpan = &b.Spans[i]
				break
			}
		}
		if linkSpan != nil {
			break
		}
	}
	if linkSpan == nil {
		t.Fatalf("no link span found after round-trip; src=%q", out)
	}

	if got := embed.LeafTileIDFromHref(linkSpan.Href); got != 5 {
		t.Errorf("href %q resolved to id %d, want 5", linkSpan.Href, got)
	}
	if linkSpan.Text != "text tile 5" {
		t.Errorf("link text = %q, want %q", linkSpan.Text, "text tile 5")
	}
	if !strings.HasPrefix(linkSpan.Href, testOrigin) {
		t.Errorf("href = %q, expected absolute (starts with %q)", linkSpan.Href, testOrigin)
	}
}

// TestDropPreservesFollowingContent asserts the drop doesn't truncate
// or duplicate the text that follows the insertion point.
func TestDropPreservesFollowingContent(t *testing.T) {
	doc := "alpha\nbravo\ncharlie\n"
	link := embed.Markdown(testOrigin, 7, "x")
	off := embed.LineEndOffset(doc, 1)
	out := embed.Insert(doc, link, off)

	if !strings.HasPrefix(out, "alpha\nbravo ") {
		t.Errorf("prefix wrong: %q", out)
	}
	if !strings.HasSuffix(out, "\ncharlie\n") {
		t.Errorf("suffix wrong: %q", out)
	}
}

// TestDropOnDocWithExistingLink makes sure dropping near an existing
// tile-link doesn't corrupt the existing link's syntax.
func TestDropOnDocWithExistingLink(t *testing.T) {
	existing := embed.Markdown(testOrigin, 3, "existing")
	doc := "intro " + existing + " trailing"
	link := embed.Markdown(testOrigin, 7, "new")

	out := embed.Insert(doc, link, len(doc))

	blocks := markdown.Parse(out)
	links := 0
	for _, b := range blocks {
		for _, sp := range b.Spans {
			if sp.Style&markdown.StyleLink != 0 {
				links++
			}
		}
	}
	if links != 2 {
		t.Errorf("expected 2 link spans, got %d; out=%q", links, out)
	}
}
