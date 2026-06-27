package markdown

import (
	"strings"

	"github.com/yuin/goldmark/ast"
	east "github.com/yuin/goldmark/extension/ast"
)

// Lower parses markdown source and lowers the goldmark AST into the document
// model (model.go). It produces the full block tree — including tables and
// nested lists — even though Layout only paints a subset today; lowering is the
// natural complete unit, so later phases are layout-only.
func Lower(src []byte) Node {
	root := parseAST(src)
	doc := Node{Kind: NodeDocument}
	doc.Children = lowerBlocks(root, src)
	return doc
}

// lowerBlocks lowers the block-level children of parent into model Nodes.
func lowerBlocks(parent ast.Node, src []byte) []Node {
	var out []Node
	for c := parent.FirstChild(); c != nil; c = c.NextSibling() {
		if n, ok := lowerBlock(c, src); ok {
			out = append(out, n)
		}
	}
	return out
}

// lowerBlock lowers a single block node. ok is false for nodes that produce no
// model node (e.g. unsupported kinds, which are skipped).
func lowerBlock(n ast.Node, src []byte) (Node, bool) {
	switch b := n.(type) {
	case *ast.Heading:
		return Node{Kind: NodeHeading, Level: b.Level, Spans: lowerInline(b, src)}, true
	case *ast.Paragraph:
		return Node{Kind: NodeParagraph, Spans: lowerInline(b, src)}, true
	case *ast.TextBlock:
		// Tight list items wrap their inline content in a TextBlock rather than
		// a Paragraph; treat it as a paragraph for layout.
		return Node{Kind: NodeParagraph, Spans: lowerInline(b, src)}, true
	case *ast.FencedCodeBlock:
		return Node{Kind: NodeCodeBlock, Lang: string(b.Language(src)),
			Spans: []Span{{Text: linesText(b, src), Style: StyleCode}}}, true
	case *ast.CodeBlock:
		return Node{Kind: NodeCodeBlock,
			Spans: []Span{{Text: linesText(b, src), Style: StyleCode}}}, true
	case *ast.Blockquote:
		return Node{Kind: NodeBlockQuote, Children: lowerBlocks(b, src)}, true
	case *ast.List:
		return lowerList(b, src), true
	case *ast.ThematicBreak:
		return Node{Kind: NodeThematicBreak}, true
	case *east.Table:
		return lowerTable(b, src), true
	}
	return Node{}, false
}

// lowerList lowers a list and its items, carrying ordered/start and per-item
// task-checkbox state.
func lowerList(l *ast.List, src []byte) Node {
	out := Node{Kind: NodeList, Ordered: l.IsOrdered(), Tight: l.IsTight, Start: l.Start}
	if !l.IsOrdered() {
		out.Start = 0
	}
	for c := l.FirstChild(); c != nil; c = c.NextSibling() {
		li, ok := c.(*ast.ListItem)
		if !ok {
			continue
		}
		item := Node{Kind: NodeListItem, Children: lowerBlocks(li, src)}
		item.Checked = taskState(li)
		out.Children = append(out.Children, item)
	}
	return out
}

// taskState returns the GFM task-checkbox state for a list item, or nil if the
// item isn't a task. The checkbox is the first inline child of the item's first
// (text) block.
func taskState(li *ast.ListItem) *bool {
	first := li.FirstChild()
	if first == nil {
		return nil
	}
	for c := first.FirstChild(); c != nil; c = c.NextSibling() {
		if cb, ok := c.(*east.TaskCheckBox); ok {
			v := cb.IsChecked
			return &v
		}
	}
	return nil
}

// lowerTable lowers a GFM table into NodeTable > NodeTableRow > NodeTableCell,
// with the header row first and per-column alignment.
func lowerTable(t *east.Table, src []byte) Node {
	out := Node{Kind: NodeTable}
	for _, a := range t.Alignments {
		out.Align = append(out.Align, lowerAlign(a))
	}
	for c := t.FirstChild(); c != nil; c = c.NextSibling() {
		switch row := c.(type) {
		case *east.TableHeader:
			out.Children = append(out.Children, lowerTableRow(row, src))
		case *east.TableRow:
			out.Children = append(out.Children, lowerTableRow(row, src))
		}
	}
	return out
}

func lowerTableRow(row ast.Node, src []byte) Node {
	out := Node{Kind: NodeTableRow}
	for c := row.FirstChild(); c != nil; c = c.NextSibling() {
		if cell, ok := c.(*east.TableCell); ok {
			out.Children = append(out.Children, Node{Kind: NodeTableCell, Spans: lowerInline(cell, src)})
		}
	}
	return out
}

func lowerAlign(a east.Alignment) Alignment {
	switch a {
	case east.AlignLeft:
		return AlignLeft
	case east.AlignCenter:
		return AlignCenter
	case east.AlignRight:
		return AlignRight
	}
	return AlignNone
}

// lowerInline flattens a block's inline children into a []Span, folding
// emphasis nesting into Style bits. parent is the block (or inline container)
// whose children are walked.
func lowerInline(parent ast.Node, src []byte) []Span {
	var spans []Span
	appendInline(&spans, parent, src, StyleNone, "")
	return spans
}

// appendInline walks n's inline children, accumulating spans under the given
// inherited style and link href.
func appendInline(out *[]Span, n ast.Node, src []byte, style SpanStyle, href string) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch t := c.(type) {
		case *ast.Text:
			text := string(t.Segment.Value(src))
			if text != "" {
				// The segment IS this run's source slice, so SrcLen == len(text):
				// a verbatim run the caret maps into linearly. This holds inside
				// bold/italic/links too — goldmark consumes the markers, and the
				// child Text node's segment covers only the visible text.
				*out = append(*out, Span{Text: text, Style: style, Href: href,
					SrcStart: t.Segment.Start, SrcLen: t.Segment.Stop - t.Segment.Start})
			}
			// A hard line break (two trailing spaces / backslash) forces a new
			// line — carried as a "\n" sentinel span the layout turns into a
			// break. A soft break is just a space. Both are synthetic (SrcLen 0,
			// opaque to the caret); anchor them at the source position just past
			// the preceding text so a caret there lands sensibly.
			if t.HardLineBreak() {
				*out = append(*out, Span{Text: "\n", Style: style, Href: href, SrcStart: t.Segment.Stop})
			} else if t.SoftLineBreak() {
				*out = append(*out, Span{Text: " ", Style: style, Href: href, SrcStart: t.Segment.Stop})
			}
		case *ast.String:
			*out = append(*out, Span{Text: string(t.Value), Style: style, Href: href})
		case *ast.Emphasis:
			s := style
			if t.Level == 2 {
				s |= StyleBold
			} else {
				s |= StyleItalic
			}
			appendInline(out, t, src, s, href)
		case *east.Strikethrough:
			appendInline(out, t, src, style|StyleStrike, href)
		case *ast.CodeSpan:
			*out = append(*out, Span{Text: codeSpanText(t, src), Style: style | StyleCode, Href: href})
		case *ast.Link:
			dest := string(t.Destination)
			if img := soleImage(t); img != nil {
				// Legacy image-in-link embed form [![alt](src)](href): one
				// embed span carrying both the link href and the image src.
				*out = append(*out, imageSpan(img, src, style|StyleEmbed, dest))
			} else {
				appendInline(out, t, src, style|StyleLink, dest)
			}
		case *ast.AutoLink:
			url := string(t.URL(src))
			*out = append(*out, Span{Text: url, Style: style | StyleLink, Href: url})
		case *ast.Image:
			*out = append(*out, imageSpan(t, src, style|StyleEmbed, ""))
		case *east.TaskCheckBox:
			// Rendered as a marker by the list layout, not inline text — skip.
		default:
			// Unknown inline (raw HTML, math, ...) — fold in its text children.
			appendInline(out, c, src, style, href)
		}
	}
}

// soleImage returns the image node if link's only meaningful child is an image
// (the [![alt](src)](href) embed form), else nil.
func soleImage(link *ast.Link) *ast.Image {
	first := link.FirstChild()
	if first == nil || first.NextSibling() != nil {
		return nil
	}
	img, _ := first.(*ast.Image)
	return img
}

// imageSpan builds an embed span from an image node, deriving alt text and the
// declared ?w=&h= size from the src.
func imageSpan(img *ast.Image, src []byte, style SpanStyle, href string) Span {
	sp := Span{
		Style: style,
		Src:   string(img.Destination),
		Alt:   inlineText(img, src),
		Href:  href,
	}
	sp.W, sp.H = embedSizeFromSrc(sp.Src)
	return sp
}

// codeSpanText returns the literal text of an inline code span.
func codeSpanText(n *ast.CodeSpan, src []byte) string {
	var b strings.Builder
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if t, ok := c.(*ast.Text); ok {
			b.Write(t.Segment.Value(src))
		}
	}
	return b.String()
}

// inlineText returns the concatenated plain text of an inline subtree (used for
// image alt text).
func inlineText(n ast.Node, src []byte) string {
	var b strings.Builder
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch t := c.(type) {
		case *ast.Text:
			b.Write(t.Segment.Value(src))
		case *ast.String:
			b.Write(t.Value)
		default:
			b.WriteString(inlineText(c, src))
		}
	}
	return b.String()
}

// linesText returns the raw text of a code block's lines.
func linesText(n ast.Node, src []byte) string {
	var b strings.Builder
	lines := n.Lines()
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		b.Write(seg.Value(src))
	}
	return strings.TrimRight(b.String(), "\n")
}
