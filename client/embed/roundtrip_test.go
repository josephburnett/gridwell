package embed_test

import (
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/client/embed"
	"github.com/josephburnett/gridwell/client/markdown"
)

// TestDropRoundTripsThroughParser exercises the full chain a real drop
// follows: build the markdown, insert into doc source, parse back, and
// assert that an embed span is recognized with the right tile id and
// dimensions. If Markdown's output format ever drifts from what
// markdown.Parse expects, this catches it.
func TestDropRoundTripsThroughParser(t *testing.T) {
	doc := "first line\nsecond line\n"
	w, h := embed.Dimensions(3, 2, 64, 192, 128)
	link := embed.Markdown(5, w, h, embed.Alt("text", 5))

	// Drop at end of first line.
	off := embed.LineEndOffset(doc, 0)
	out := embed.Insert(doc, link, off)

	blocks := markdown.Parse(out)
	var embedSpan *markdown.Span
	for _, b := range blocks {
		for i := range b.Spans {
			if b.Spans[i].Style&markdown.StyleEmbed != 0 {
				embedSpan = &b.Spans[i]
				break
			}
		}
		if embedSpan != nil {
			break
		}
	}
	if embedSpan == nil {
		t.Fatalf("no embed span found after round-trip; src=%q", out)
	}

	// Resolve the href back to a tile id and confirm it matches.
	if got := embed.LeafTileIDFromHref(embedSpan.Href); got != 5 {
		t.Errorf("href %q resolved to id %d, want 5", embedSpan.Href, got)
	}

	if embedSpan.W != 192 || embedSpan.H != 128 {
		t.Errorf("embed dims = %dx%d, want 192x128", embedSpan.W, embedSpan.H)
	}

	if !strings.Contains(embedSpan.Src, "/preview/tile/5") {
		t.Errorf("src = %q, expected to contain /preview/tile/5", embedSpan.Src)
	}
}

// TestDropPreservesFollowingContent asserts the drop doesn't truncate
// or duplicate the text that follows the insertion point.
func TestDropPreservesFollowingContent(t *testing.T) {
	doc := "alpha\nbravo\ncharlie\n"
	link := embed.Markdown(7, 64, 64, "x")
	off := embed.LineEndOffset(doc, 1) // end of "bravo"
	out := embed.Insert(doc, link, off)

	if !strings.HasPrefix(out, "alpha\nbravo ") {
		t.Errorf("prefix wrong: %q", out)
	}
	if !strings.HasSuffix(out, "\ncharlie\n") {
		t.Errorf("suffix wrong: %q", out)
	}
}

// TestDropOnDocWithExistingEmbed makes sure dropping near an existing
// embed doesn't corrupt the existing link's syntax.
func TestDropOnDocWithExistingEmbed(t *testing.T) {
	existing := embed.Markdown(3, 64, 64, "existing")
	doc := "intro " + existing + " trailing"
	link := embed.Markdown(7, 64, 64, "new")

	// Drop at the very end of the doc.
	out := embed.Insert(doc, link, len(doc))

	blocks := markdown.Parse(out)
	embeds := 0
	for _, b := range blocks {
		for _, sp := range b.Spans {
			if sp.Style&markdown.StyleEmbed != 0 {
				embeds++
			}
		}
	}
	if embeds != 2 {
		t.Errorf("expected 2 embed spans, got %d; out=%q", embeds, out)
	}
}
