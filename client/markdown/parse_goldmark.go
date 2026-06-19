package markdown

import (
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	gmtext "github.com/yuin/goldmark/text"
)

// gmParser is the shared goldmark parser, configured with the GitHub-Flavored
// Markdown extensions (tables, strikethrough, task lists, autolinks). It is
// the parse half of the new pipeline; Lower (Phase 1) walks the AST it returns
// into the document model in model.go. Only the *parser* is kept — we never
// use goldmark's HTML renderer, since we paint to canvas.
var gmParser = goldmark.New(goldmark.WithExtensions(extension.GFM)).Parser()

// parseAST parses markdown source into a goldmark AST. The returned root is the
// document node; AST nodes reference src by byte segment, so the caller must
// keep src alive while walking (Lower threads it through).
func parseAST(src []byte) ast.Node {
	return gmParser.Parse(gmtext.NewReader(src))
}
