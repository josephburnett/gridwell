// Package markdown is the small layout pass used by the Gridwell client to
// render markdown into a canvas.
//
// The supported feature set is intentionally tiny: H1–H3, bold, italic,
// inline code, code blocks, blockquotes, unordered lists, paragraphs.
// Anything else falls back to plain paragraph text.
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
// fixed style; emphasis switches mark new spans. StyleLink and StyleEmbed
// are also surfaced here even though they describe link/image semantics
// rather than text styling — they share the inline-span tokenizer.
type SpanStyle uint8

const (
	StyleNone   SpanStyle = 0
	StyleBold   SpanStyle = 1 << 0
	StyleItalic SpanStyle = 1 << 1
	StyleCode   SpanStyle = 1 << 2
	StyleLink   SpanStyle = 1 << 3
	StyleEmbed  SpanStyle = 1 << 4
)

// Span is one styled run of text inside a block. For link spans, Href is
// set and Text is the link's display text. For embed spans, Src/Alt/Href
// are set; W/H carry the declared embed pixel size (from the src URL's
// ?w=&h= query parameters, if any).
type Span struct {
	Text  string
	Style SpanStyle
	Href  string
	Src   string
	Alt   string
	W, H  int
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
// (e.g., a list inside a blockquote). For Gridwell's use the simplifications
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
		// Embed: [![alt](src)](href) — image wrapped in a link. This is the
		// markdown pattern Gridwell uses for tile embeds, but the parser
		// recognises it for any markdown source.
		if s[i] == '[' && i+1 < len(s) && s[i+1] == '!' {
			if end, alt, src, href, ok := parseEmbed(s, i); ok {
				flush()
				sp := Span{Style: StyleEmbed, Alt: alt, Src: src, Href: href}
				sp.W, sp.H = embedSizeFromSrc(src)
				out = append(out, sp)
				i = end
				continue
			}
		}
		// Image alone: ![alt](src) — rendered as an embed with no href.
		if s[i] == '!' && i+1 < len(s) && s[i+1] == '[' {
			if end, alt, src, ok := parseImage(s, i); ok {
				flush()
				sp := Span{Style: StyleEmbed, Alt: alt, Src: src}
				sp.W, sp.H = embedSizeFromSrc(src)
				out = append(out, sp)
				i = end
				continue
			}
		}
		// Plain link: [text](href).
		if s[i] == '[' {
			if end, text, href, ok := parseLink(s, i); ok {
				flush()
				out = append(out, Span{Text: text, Style: style | StyleLink, Href: href})
				i = end
				continue
			}
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

// parseEmbed attempts to read [![alt](src)](href) starting at s[start]. On
// success ok is true and end is the index one past the closing ).
func parseEmbed(s string, start int) (end int, alt, src, href string, ok bool) {
	if start >= len(s) || s[start] != '[' {
		return 0, "", "", "", false
	}
	imgEnd, alt, src, imgOK := parseImage(s, start+1)
	if !imgOK {
		return 0, "", "", "", false
	}
	if imgEnd >= len(s) || s[imgEnd] != ']' {
		return 0, "", "", "", false
	}
	if imgEnd+1 >= len(s) || s[imgEnd+1] != '(' {
		return 0, "", "", "", false
	}
	hrefStart := imgEnd + 2
	close := strings.IndexByte(s[hrefStart:], ')')
	if close < 0 {
		return 0, "", "", "", false
	}
	return hrefStart + close + 1, alt, src, s[hrefStart : hrefStart+close], true
}

// parseImage reads ![alt](src) starting at s[start].
func parseImage(s string, start int) (end int, alt, src string, ok bool) {
	if start+1 >= len(s) || s[start] != '!' || s[start+1] != '[' {
		return 0, "", "", false
	}
	altStart := start + 2
	closeBracket := strings.IndexByte(s[altStart:], ']')
	if closeBracket < 0 {
		return 0, "", "", false
	}
	alt = s[altStart : altStart+closeBracket]
	after := altStart + closeBracket + 1
	if after >= len(s) || s[after] != '(' {
		return 0, "", "", false
	}
	srcStart := after + 1
	closeParen := strings.IndexByte(s[srcStart:], ')')
	if closeParen < 0 {
		return 0, "", "", false
	}
	return srcStart + closeParen + 1, alt, s[srcStart : srcStart+closeParen], true
}

// parseLink reads [text](href) starting at s[start]. Does not handle nested
// brackets in text; that's fine for the small surface we target.
func parseLink(s string, start int) (end int, text, href string, ok bool) {
	if start >= len(s) || s[start] != '[' {
		return 0, "", "", false
	}
	textStart := start + 1
	closeBracket := strings.IndexByte(s[textStart:], ']')
	if closeBracket < 0 {
		return 0, "", "", false
	}
	text = s[textStart : textStart+closeBracket]
	after := textStart + closeBracket + 1
	if after >= len(s) || s[after] != '(' {
		return 0, "", "", false
	}
	hrefStart := after + 1
	closeParen := strings.IndexByte(s[hrefStart:], ')')
	if closeParen < 0 {
		return 0, "", "", false
	}
	return hrefStart + closeParen + 1, text, s[hrefStart : hrefStart+closeParen], true
}

// embedSizeFromSrc parses ?w= and ?h= query parameters out of an embed src
// URL. Returns (0, 0) if either is missing — the renderer falls back to
// its own default in that case.
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

// Wrap takes a block's spans and wraps them into lines of at most maxWidth
// pixels. measure returns the rendered pixel width of a given span (the
// callback may inspect Text, Style, Src, W/H to compute it).
//
// Wrap is deliberately byte-greedy on word boundaries; CJK and emoji are
// not considered in v1. Trailing whitespace is preserved within a wrapped
// line; leading whitespace on continuation lines is trimmed. Embed spans
// (StyleEmbed) are atomic — they're never split into sub-tokens.
func Wrap(spans []Span, maxWidth float64, measure func(sp Span) float64) [][]Span {
	// Tokenize text spans on whitespace boundaries while preserving span
	// style. Each text-token is a "run of non-space" or "single space".
	// Embed spans pass through as a single atomic token.
	var tokens []Span
	for _, sp := range spans {
		if sp.Style&StyleEmbed != 0 {
			tokens = append(tokens, sp)
			continue
		}
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
			tok := sp
			tok.Text = sp.Text[i:j]
			tokens = append(tokens, tok)
			i = j
		}
	}

	var lines [][]Span
	var cur []Span
	curWidth := 0.0
	for _, tk := range tokens {
		w := measure(tk)
		isSpace := tk.Style&StyleEmbed == 0 && strings.TrimSpace(tk.Text) == ""
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
		cur = append(cur, tk)
		curWidth += w
	}
	if len(cur) > 0 {
		lines = append(lines, cur)
	}
	return lines
}
