// alttext.go — alt-text derivation for text tiles. Moved here from
// client/markdown (2026-08-15): the localdb STORE auto-titles text tiles
// with it, and persistence importing the client tree was the known
// layering wrinkle (ARCHITECTURE §4.2) — the same arrow the plugin
// decoupling forbids. doctype is the neutral home for text-document
// semantics shared across the seam (Renderable, IsOrg, and now this).
// The parse dialect is GFM, matching the client renderer's parser, so
// the derived title always agrees with what the rendered view shows.

package doctype

import (
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	gmtext "github.com/yuin/goldmark/text"
)

// altParser parses with the same dialect the client renders (GFM);
// renderer options are irrelevant here — only the AST is read.
var altParser = goldmark.New(goldmark.WithExtensions(extension.GFM))

// AltFromSource derives a short, one-line alt-text from a markdown document:
// the plain text of the first block, with markdown markers stripped (so
// "# Heading" becomes "Heading"), all whitespace runs (newlines included)
// collapsed to single spaces, clamped to altMaxLen runes. Returns "" for empty
// or content-free input. The store uses it to auto-title text tiles.
//
// Collapsing to one line matters: a code-block-first doc would otherwise
// yield a multi-line alt.
func AltFromSource(src string) string {
	source := []byte(src)
	root := altParser.Parser().Parse(gmtext.NewReader(source))
	for b := root.FirstChild(); b != nil; b = b.NextSibling() {
		s := strings.Join(strings.Fields(blockPlainText(b, source)), " ")
		if s == "" {
			continue
		}
		return clampRunes(s, altMaxLen)
	}
	return ""
}

// blockPlainText is the concatenated plain text of one block-level AST node:
// inline text and code-span text verbatim, code-block lines raw, images (and
// everything inside them) skipped.
func blockPlainText(n ast.Node, src []byte) string {
	var b strings.Builder
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch t := c.(type) {
		case *ast.Image:
			return ast.WalkSkipChildren, nil
		case *ast.Text:
			b.Write(t.Segment.Value(src))
			if t.SoftLineBreak() || t.HardLineBreak() {
				b.WriteByte(' ')
			}
		case *ast.String:
			b.Write(t.Value)
		case *ast.AutoLink:
			b.Write(t.Label(src))
		case *ast.FencedCodeBlock, *ast.CodeBlock:
			lines := c.Lines()
			for i := 0; i < lines.Len(); i++ {
				seg := lines.At(i)
				b.Write(seg.Value(src))
			}
		}
		return ast.WalkContinue, nil
	})
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
