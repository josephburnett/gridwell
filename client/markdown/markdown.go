// Package markdown is the Gridwell client's markdown engine. It parses GFM
// (via goldmark — see parse_goldmark.go), lowers the AST into a document model
// (model.go), and lays it out into positioned draw ops (layout.go) that the
// wasm canvas renderer paints. This file holds the shared inline types, the
// alt-text derivation, and the embed-size helper.
//
// The package is pure Go; nothing here touches syscall/js, so the parse →
// lower → layout pipeline is exercised entirely by `go test`.
package markdown

import "strings"

// SpanStyle bits combine for inline formatting. StyleLink / StyleEmbed describe
// link/image semantics rather than text styling but share the inline span type.
type SpanStyle uint8

const (
	StyleNone   SpanStyle = 0
	StyleBold   SpanStyle = 1 << 0
	StyleItalic SpanStyle = 1 << 1
	StyleCode   SpanStyle = 1 << 2
	StyleLink   SpanStyle = 1 << 3
	StyleEmbed  SpanStyle = 1 << 4
	StyleStrike SpanStyle = 1 << 5
)

// Span is one styled run of inline text. For link spans Href is set; for embed
// (image / tile-link) spans Src/Alt/Href are set and W/H carry the declared
// embed pixel size (from the src URL's ?w=&h=, if any).
type Span struct {
	Text  string
	Style SpanStyle
	Href  string
	Src   string
	Alt   string
	W, H  int
}

// AltFromSource derives a short, one-line alt-text from a markdown document:
// the plain text of the first block, with markdown markers stripped (so
// "# Heading" becomes "Heading"), all whitespace runs (newlines included)
// collapsed to single spaces, clamped to altMaxLen runes. Returns "" for empty
// or content-free input. Used to label tile-embed links.
//
// Collapsing to one line matters: a code-block-first doc would otherwise yield
// a multi-line alt, and a newline in alt text breaks the generated
// [alt](href) embed link.
func AltFromSource(src string) string {
	for _, b := range Lower([]byte(src)).Children {
		s := strings.Join(strings.Fields(blockText(b)), " ")
		if s == "" {
			continue
		}
		return clampRunes(s, altMaxLen)
	}
	return ""
}

// blockText is the concatenated plain text of a block (embeds skipped),
// recursing into child blocks.
func blockText(n Node) string {
	var b strings.Builder
	for _, sp := range n.Spans {
		if sp.Style&StyleEmbed != 0 {
			continue
		}
		b.WriteString(sp.Text)
	}
	for _, c := range n.Children {
		b.WriteString(blockText(c))
	}
	return b.String()
}

const altMaxLen = 100

func clampRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

// embedSizeFromSrc parses ?w= and ?h= out of an embed src URL. Returns (0, 0)
// if either is missing — the layout falls back to its default size.
func embedSizeFromSrc(src string) (int, int) {
	_, query, ok := strings.Cut(src, "?")
	if !ok {
		return 0, 0
	}
	w, h := 0, 0
	for kv := range strings.SplitSeq(query, "&") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch k {
		case "w":
			w = atoiSafe(v)
		case "h":
			h = atoiSafe(v)
		}
	}
	return w, h
}

func atoiSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
		if n > 1<<20 {
			return 0
		}
	}
	return n
}
