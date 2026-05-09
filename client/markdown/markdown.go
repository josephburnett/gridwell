// Package markdown is the small layout pass used by the Ascent client to
// render markdown into a canvas.
//
// We do not use a full CommonMark parser because the rendering surface is a
// canvas and the supported feature set (per spec §8.1) is intentionally tiny:
// H1–H3, bold, italic, inline code, code blocks, blockquotes, unordered
// lists, paragraphs. Anything else falls back to plain paragraph text.
//
// The package is pure Go; output is a slice of layout primitives that the
// canvas rendering code translates into fillText / fillRect calls.
package markdown

import "strings"

// BlockKind classifies a layout block.
type BlockKind int

const (
	BlockParagraph BlockKind = iota
	BlockHeading1
	BlockHeading2
	BlockHeading3
	BlockBlockquote
	BlockListItem
	BlockCode // fenced code block; spans is one Span with raw text
	BlockBlank // blank line (vertical spacing only)
)

// SpanStyle bits combine for inline formatting. A single span carries one
// fixed style; emphasis switches mark new spans.
type SpanStyle uint8

const (
	StyleNone   SpanStyle = 0
	StyleBold   SpanStyle = 1 << 0
	StyleItalic SpanStyle = 1 << 1
	StyleCode   SpanStyle = 1 << 2
)

// Span is one styled run of text inside a block.
type Span struct {
	Text  string
	Style SpanStyle
}

// Block is the unit of vertical layout: a sequence of inline spans plus a
// block kind that determines font size, indent, and any decoration (bullet,
// quote bar).
type Block struct {
	Kind  BlockKind
	Spans []Span
}

// Parse splits source into Blocks. Inline parsing happens per-block.
//
// The parser is line-based and does not handle nested block structure
// (e.g., a list inside a blockquote). For Ascent's use the simplifications
// are acceptable; nothing here goes wrong on input it does not understand,
// it just renders as a paragraph.
func Parse(src string) []Block {
	lines := strings.Split(src, "\n")
	var out []Block
	inCode := false
	var code strings.Builder
	for _, line := range lines {
		if strings.HasPrefix(line, "```") {
			if inCode {
				out = append(out, Block{Kind: BlockCode, Spans: []Span{{Text: code.String(), Style: StyleCode}}})
				code.Reset()
				inCode = false
			} else {
				inCode = true
			}
			continue
		}
		if inCode {
			code.WriteString(line)
			code.WriteString("\n")
			continue
		}
		switch {
		case strings.TrimSpace(line) == "":
			out = append(out, Block{Kind: BlockBlank})
		case strings.HasPrefix(line, "### "):
			out = append(out, Block{Kind: BlockHeading3, Spans: parseInline(line[4:])})
		case strings.HasPrefix(line, "## "):
			out = append(out, Block{Kind: BlockHeading2, Spans: parseInline(line[3:])})
		case strings.HasPrefix(line, "# "):
			out = append(out, Block{Kind: BlockHeading1, Spans: parseInline(line[2:])})
		case strings.HasPrefix(line, "> "):
			out = append(out, Block{Kind: BlockBlockquote, Spans: parseInline(line[2:])})
		case strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* "):
			out = append(out, Block{Kind: BlockListItem, Spans: parseInline(line[2:])})
		default:
			out = append(out, Block{Kind: BlockParagraph, Spans: parseInline(line)})
		}
	}
	if inCode {
		// Unterminated code fence: emit what we have.
		out = append(out, Block{Kind: BlockCode, Spans: []Span{{Text: code.String(), Style: StyleCode}}})
	}
	return out
}

// parseInline tokenizes inline emphasis. Supports:
//   `code`         -> StyleCode
//   **bold**       -> StyleBold
//   *italic* / _italic_ -> StyleItalic
//
// Bold/italic combine if nested but the implementation here keeps things
// simple by treating each marker as a flat toggle rather than building a
// nested AST. Mismatched markers are passed through as literal text.
func parseInline(s string) []Span {
	var out []Span
	cur := strings.Builder{}
	style := StyleNone

	flush := func() {
		if cur.Len() > 0 {
			out = append(out, Span{Text: cur.String(), Style: style})
			cur.Reset()
		}
	}

	for i := 0; i < len(s); {
		// Inline code: backtick to backtick.
		if s[i] == '`' {
			flush()
			end := strings.IndexByte(s[i+1:], '`')
			if end < 0 {
				cur.WriteByte('`')
				i++
				continue
			}
			out = append(out, Span{Text: s[i+1 : i+1+end], Style: StyleCode})
			i = i + end + 2
			continue
		}
		// Bold: ** ... **
		if i+1 < len(s) && s[i] == '*' && s[i+1] == '*' {
			flush()
			style ^= StyleBold
			i += 2
			continue
		}
		// Italic: single * or _
		if s[i] == '*' || s[i] == '_' {
			flush()
			style ^= StyleItalic
			i++
			continue
		}
		cur.WriteByte(s[i])
		i++
	}
	flush()
	return out
}

// Wrap takes a block's spans and wraps them into lines of at most maxWidth
// pixels. measureWidth returns the rendered pixel width of (text, style).
//
// Wrap is deliberately byte-greedy on word boundaries; CJK and emoji are
// not considered in v1. Trailing whitespace is preserved within a wrapped
// line; leading whitespace on continuation lines is trimmed.
func Wrap(spans []Span, maxWidth float64, measureWidth func(text string, style SpanStyle) float64) [][]Span {
	type token struct {
		text  string
		style SpanStyle
	}
	// Tokenize spans on whitespace boundaries while preserving each span's
	// style. Each token is a "run of non-space" or "single space".
	var tokens []token
	for _, sp := range spans {
		i := 0
		for i < len(sp.Text) {
			j := i
			if sp.Text[i] == ' ' || sp.Text[i] == '\t' {
				for j < len(sp.Text) && (sp.Text[j] == ' ' || sp.Text[j] == '\t') {
					j++
				}
			} else {
				for j < len(sp.Text) && sp.Text[j] != ' ' && sp.Text[j] != '\t' {
					j++
				}
			}
			tokens = append(tokens, token{text: sp.Text[i:j], style: sp.Style})
			i = j
		}
	}

	var lines [][]Span
	var cur []Span
	curWidth := 0.0
	for _, tk := range tokens {
		w := measureWidth(tk.text, tk.style)
		isSpace := strings.TrimSpace(tk.text) == ""
		// If a non-space token would overflow and there's already content on
		// the line, wrap before this token.
		if !isSpace && curWidth+w > maxWidth && len(cur) > 0 {
			lines = append(lines, cur)
			cur = nil
			curWidth = 0
		}
		// Skip leading whitespace at start of line.
		if isSpace && len(cur) == 0 {
			continue
		}
		cur = append(cur, Span{Text: tk.text, Style: tk.style})
		curWidth += w
	}
	if len(cur) > 0 {
		lines = append(lines, cur)
	}
	return lines
}
