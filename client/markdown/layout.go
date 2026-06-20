package markdown

import "strings"

// LayoutStyle holds the metrics the layout pass needs. The wasm renderer fills
// it with the app's real sizes (so the canvas look is preserved); tests pass a
// simple deterministic style. All values are in logical (pre-zoom) pixels.
type LayoutStyle struct {
	BaseFontPx  float64    // paragraph / default
	HeadingPx   [7]float64 // index 1..6 (0 unused)
	CodeFontPx  float64    // code blocks + inline code
	LineSpacing float64    // line height = fontPx * LineSpacing
	BlockGap    float64    // vertical gap between sibling blocks
	PadX        float64    // left content padding
	ListIndent  float64    // indent per list depth
	QuoteIndent float64    // indent per blockquote depth
	QuoteBarW   float64    // width of the blockquote bar
	CodePadX    float64    // code block inner horizontal padding
	CodePadY    float64    // code block inner vertical padding
	EmbedW      float64    // default inline embed width when unspecified
	EmbedH      float64    // default inline embed height
}

// DefaultLayoutStyle is a self-consistent style used by tests and as a
// starting point; the wasm renderer overrides the fields it cares about.
func DefaultLayoutStyle() LayoutStyle {
	return LayoutStyle{
		BaseFontPx:  16,
		HeadingPx:   [7]float64{0, 32, 26, 21, 18, 16, 15},
		CodeFontPx:  14,
		LineSpacing: 1.4,
		BlockGap:    8,
		PadX:        8,
		ListIndent:  24,
		QuoteIndent: 16,
		QuoteBarW:   3,
		CodePadX:    8,
		CodePadY:    6,
		EmbedW:      144,
		EmbedH:      144,
	}
}

// EmbedFunc classifies a span as a tile embed (drawn as an atomic OpEmbed)
// rather than flowing text. The wasm caller passes embed.SpanIsEmbed (which
// knows tile-link hrefs); tests pass a stub. Keeping it a callback avoids an
// import cycle (client/embed imports client/markdown).
type EmbedFunc func(sp Span) bool

// Layout turns a lowered document into positioned draw ops at the given content
// width. It is pure: all canvas interaction is deferred to the painter, which
// just walks LayoutResult.Ops and scales them. width is the content width in
// logical px (the area between the left pad and the right edge).
func Layout(doc Node, m Measure, isEmbed EmbedFunc, width float64, style LayoutStyle) LayoutResult {
	lw := &layoutWriter{m: m, isEmbed: isEmbed, style: style}
	y := lw.blocks(doc.Children, style.PadX, 0, width-2*style.PadX, 0)
	return LayoutResult{Ops: lw.ops, Height: y}
}

// layoutWriter accumulates ops while walking the block tree.
type layoutWriter struct {
	m       Measure
	isEmbed EmbedFunc
	style   LayoutStyle
	ops     []DrawOp
}

// blocks lays out a sequence of sibling blocks starting at (x, y) within the
// given available width, returning the y just past the last block. x is the
// left edge for this nesting level; avail is the usable width from x.
func (w *layoutWriter) blocks(nodes []Node, x, y, avail float64, depth int) float64 {
	for i, n := range nodes {
		if i > 0 {
			y += w.style.BlockGap
		}
		y = w.block(n, x, y, avail, depth)
	}
	return y
}

func (w *layoutWriter) block(n Node, x, y, avail float64, depth int) float64 {
	switch n.Kind {
	case NodeHeading:
		fp := w.style.BaseFontPx
		if n.Level >= 1 && n.Level <= 6 {
			fp = w.style.HeadingPx[n.Level]
		}
		return w.inline(n.Spans, x, y, avail, fp, ColorHeading, false)
	case NodeParagraph:
		return w.inline(n.Spans, x, y, avail, w.style.BaseFontPx, ColorText, false)
	case NodeCodeBlock:
		return w.codeBlock(n, x, y, avail)
	case NodeBlockQuote:
		return w.blockquote(n, x, y, avail, depth)
	case NodeList:
		return w.list(n, x, y, avail, depth)
	case NodeThematicBreak:
		mid := y + w.style.BlockGap/2
		w.ops = append(w.ops, DrawOp{Kind: OpRule, X: x, Y: mid, W: avail, H: 1, Color: ColorRuleLine})
		return y + w.style.BlockGap
	}
	return y
}

// codeBlock draws a background panel and each raw line as monospace text.
func (w *layoutWriter) codeBlock(n Node, x, y, avail float64) float64 {
	body := ""
	if len(n.Spans) > 0 {
		body = n.Spans[0].Text
	}
	lines := strings.Split(body, "\n")
	fp := w.style.CodeFontPx
	lh := fp * w.style.LineSpacing
	height := w.style.CodePadY*2 + lh*float64(len(lines))
	w.ops = append(w.ops, DrawOp{Kind: OpRect, X: x, Y: y, W: avail, H: height, Color: ColorCodeBg})
	ty := y + w.style.CodePadY
	for _, ln := range lines {
		w.ops = append(w.ops, DrawOp{Kind: OpText, X: x + w.style.CodePadX, Y: ty,
			Text: ln, FontPx: fp, Style: StyleCode, Mono: true, Color: ColorCode})
		ty += lh
	}
	return y + height
}

// blockquote draws a left bar and lays its children out indented.
func (w *layoutWriter) blockquote(n Node, x, y, avail float64, depth int) float64 {
	innerX := x + w.style.QuoteIndent
	innerAvail := avail - w.style.QuoteIndent
	top := y
	endY := w.blocks(n.Children, innerX, y, innerAvail, depth+1)
	w.ops = append(w.ops, DrawOp{Kind: OpRect, X: x, Y: top, W: w.style.QuoteBarW, H: endY - top, Color: ColorQuoteBar})
	return endY
}

// list lays out a single level of list items with markers; nested lists recurse
// through each item's child blocks.
func (w *layoutWriter) list(n Node, x, y, avail float64, depth int) float64 {
	markerW := w.style.ListIndent
	itemX := x + markerW
	itemAvail := avail - markerW
	num := n.Start
	if n.Ordered && num == 0 {
		num = 1
	}
	for i, item := range n.Children {
		if i > 0 && !n.Tight {
			y += w.style.BlockGap
		}
		marker := w.marker(n, item, num)
		w.ops = append(w.ops, DrawOp{Kind: OpText, X: x + markerW*0.25, Y: y,
			Text: marker, FontPx: w.style.BaseFontPx, Color: ColorMuted})
		y = w.blocks(item.Children, itemX, y, itemAvail, depth+1)
		num++
	}
	return y
}

// marker returns the list marker text for an item: a bullet, an ordinal, or a
// task checkbox.
func (w *layoutWriter) marker(list, item Node, num int) string {
	if item.Checked != nil {
		if *item.Checked {
			return "☑" // ballot box with check
		}
		return "☐" // ballot box
	}
	if list.Ordered {
		return itoa(num) + "."
	}
	return "•" // bullet
}

// inline wraps a block's spans into lines within avail, emitting OpText (and
// atomic OpEmbed) ops, and returns the y just past the last line.
func (w *layoutWriter) inline(spans []Span, x, y, avail, fontPx float64, color ColorRole, mono bool) float64 {
	tokens := w.tokenize(spans, fontPx, mono)
	lineH := fontPx * w.style.LineSpacing

	var line []inlineToken
	var lineW, lineMaxH float64
	flush := func() {
		if len(line) == 0 {
			return
		}
		h := lineH
		if lineMaxH > h {
			h = lineMaxH
		}
		cx := x
		for _, tk := range line {
			if tk.atomic {
				alt := tk.span.Alt
				if alt == "" {
					alt = tk.span.Text
				}
				w.ops = append(w.ops, DrawOp{Kind: OpEmbed, X: cx, Y: y, W: tk.width, H: tk.height,
					Href: tk.span.Href, Src: tk.span.Src, Alt: alt})
				cx += tk.width
				continue
			}
			col := color
			if tk.span.Style&StyleLink != 0 {
				col = ColorLink
			} else if tk.span.Style&StyleCode != 0 {
				col = ColorCode
				// Inline code gets its own background chip (a code block's
				// panel is drawn separately by codeBlock()).
				w.ops = append(w.ops, DrawOp{Kind: OpRect, X: cx - 2, Y: y, W: tk.width + 4, H: h, Color: ColorInlineCodeBg})
			}
			w.ops = append(w.ops, DrawOp{Kind: OpText, X: cx, Y: y, Text: tk.span.Text,
				FontPx: fontPx, Style: tk.span.Style, Mono: mono || tk.span.Style&StyleCode != 0,
				Color: col, Href: tk.span.Href})
			cx += tk.width
		}
		y += h
		line = nil
		lineW, lineMaxH = 0, 0
	}

	for _, tk := range tokens {
		if !tk.isSpace && lineW+tk.width > avail && len(line) > 0 {
			flush()
		}
		if tk.isSpace && len(line) == 0 {
			continue // drop leading whitespace
		}
		line = append(line, tk)
		lineW += tk.width
		if tk.atomic && tk.height > lineMaxH {
			lineMaxH = tk.height
		}
	}
	flush()
	return y
}

// inlineToken is one atomic unit of inline flow: a word, a space run, or an
// atomic embed/image block.
type inlineToken struct {
	span   Span
	width  float64
	isSpace bool
	atomic bool
	height float64
}

// tokenize splits spans into flow tokens: text spans break on whitespace
// boundaries (so wrapping can happen between words); embed spans pass through
// as a single atomic block sized by the style defaults or the span's W/H.
func (w *layoutWriter) tokenize(spans []Span, fontPx float64, mono bool) []inlineToken {
	var toks []inlineToken
	for _, sp := range spans {
		if w.isEmbed != nil && w.isEmbed(sp) {
			// Coalesce consecutive embed spans that share the same non-empty
			// href into one embed: a tile link whose text is several inline
			// spans (e.g. "[a **b**](/5)") is ONE embed, not several.
			if sp.Href != "" && len(toks) > 0 {
				if last := &toks[len(toks)-1]; last.atomic && last.span.Href == sp.Href {
					last.span.Text += sp.Text
					continue
				}
			}
			ew, eh := w.embedSize(sp)
			toks = append(toks, inlineToken{span: sp, width: ew, height: eh, atomic: true})
			continue
		}
		// Links are atomic-ish only for embeds; ordinary links still wrap by
		// word so long link text doesn't overflow.
		i := 0
		for i < len(sp.Text) {
			j := i
			isSpace := sp.Text[i] == ' ' || sp.Text[i] == '\t'
			if isSpace {
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
			toks = append(toks, inlineToken{
				span:    tok,
				width:   w.m(tok.Text, fontPx, sp.Style, mono || sp.Style&StyleCode != 0),
				isSpace: isSpace,
			})
			i = j
		}
	}
	return toks
}

// embedSize resolves an embed span's logical size, preferring its declared
// W/H, falling back to the style defaults.
func (w *layoutWriter) embedSize(sp Span) (float64, float64) {
	ew, eh := float64(sp.W), float64(sp.H)
	if ew <= 0 {
		ew = w.style.EmbedW
	}
	if eh <= 0 {
		eh = w.style.EmbedH
	}
	return ew, eh
}

// itoa is a tiny non-allocating-ish positive-int formatter for list ordinals.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
