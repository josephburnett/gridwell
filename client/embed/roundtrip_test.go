package embed_test

import (
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/client/embed"
	"github.com/josephburnett/gridwell/client/markdown"
)

const testOrigin = "http://localhost:8080"

// linkSpans collects every StyleLink span in a lowered document (depth-first).
func linkSpans(n markdown.Node) []markdown.Span {
	var out []markdown.Span
	for _, sp := range n.Spans {
		if sp.Style&markdown.StyleLink != 0 {
			out = append(out, sp)
		}
	}
	for _, c := range n.Children {
		out = append(out, linkSpans(c)...)
	}
	return out
}

// TestDropRoundTripsThroughParser exercises the full chain a real drop
// follows: build the markdown, insert into doc source, parse back, and
// assert that the link href resolves to the right tile id. The plain
// link form means the parser produces a StyleLink span rather than a
// StyleEmbed; the rendered-mode renderer is what paints it as a
// preview by intercepting the href pattern.
func TestDropRoundTripsThroughParser(t *testing.T) {
	doc := "first line\nsecond line\n"
	link := embed.Markdown(testOrigin, "5", embed.DefaultAlt("text", "5"))

	off := embed.LineEndOffset(doc, 0)
	out := embed.Insert(doc, link, off)

	spans := linkSpans(markdown.Lower([]byte(out)))
	if len(spans) == 0 {
		t.Fatalf("no link span found after round-trip; src=%q", out)
	}
	linkSpan := &spans[0]

	if got := embed.LeafTileIDFromHref(linkSpan.Href); got != "5" {
		t.Errorf("href %q resolved to id %q, want 5", linkSpan.Href, got)
	}
	if linkSpan.Text != "text tile 5" {
		t.Errorf("link text = %q, want %q", linkSpan.Text, "text tile 5")
	}
	if !strings.HasPrefix(linkSpan.Href, testOrigin) {
		t.Errorf("href = %q, expected absolute (starts with %q)", linkSpan.Href, testOrigin)
	}
}

// TestDropFromQualifiedTileResolvesBack is the regression test for the
// "embed renders as an orange 'missing' square (and won't descend)" bug. A real
// drop passes the source tile's QUALIFIED id ("uuid/42"); HrefForTile strips the
// uuid for a human-readable link, so the client must re-qualify on read — with
// the embedding doc's plugin uuid — to match the (qualified) tile-cache keys.
// The other round-trip tests only ever used a bare id ("5"), so the uuid-strip
// was a no-op and this strip-then-requalify gap was invisible; this exercises
// the full chain from a qualified source.
func TestDropFromQualifiedTileResolvesBack(t *testing.T) {
	const anchor = "abc-uuid"
	const src = anchor + "/42"
	link := embed.Markdown(testOrigin, src, embed.DefaultAlt("text", src))

	doc := "first line\n"
	out := embed.Insert(doc, link, embed.LineEndOffset(doc, 0))

	spans := linkSpans(markdown.Lower([]byte(out)))
	if len(spans) == 0 {
		t.Fatalf("no link span found after round-trip; src=%q", out)
	}
	if got := embed.ResolveEmbedTileID(anchor, spans[0].Href); got != src {
		t.Errorf("qualified embed resolved to %q, want %q (href=%q)",
			got, src, spans[0].Href)
	}
}

// TestDropPreservesFollowingContent asserts the drop doesn't truncate
// or duplicate the text that follows the insertion point.
func TestDropPreservesFollowingContent(t *testing.T) {
	doc := "alpha\nbravo\ncharlie\n"
	link := embed.Markdown(testOrigin, "7", "x")
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
	existing := embed.Markdown(testOrigin, "3", "existing")
	doc := "intro " + existing + " trailing"
	link := embed.Markdown(testOrigin, "7", "new")

	out := embed.Insert(doc, link, len(doc))

	if links := len(linkSpans(markdown.Lower([]byte(out)))); links != 2 {
		t.Errorf("expected 2 link spans, got %d; out=%q", links, out)
	}
}
