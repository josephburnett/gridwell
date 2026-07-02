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
	TablePadX   float64    // table cell inner horizontal padding
	TablePadY   float64    // table cell inner vertical padding
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
		TablePadX:   6,
		TablePadY:   3,
		EmbedW:      144,
		EmbedH:      144,
	}
}

// AtomKind classifies an inline span as an atomic (non-flowing) block, or as
// ordinary flowing text.
type AtomKind int

const (
	// AtomNone — the span flows as text (ordinary text or an external link).
	AtomNone AtomKind = iota
	// AtomEmbed — a native tile preview (a tile-link href, or the legacy
	// image-in-link form): drawn as an atomic OpEmbed.
	AtomEmbed
	// AtomImage — a real external image (![alt](src) with no tile href): drawn
	// as an atomic OpImage (fetched + painted by the renderer).
	AtomImage
)

// ClassifyFunc classifies a span into an AtomKind. The wasm caller builds it
// from embed.LeafTileIDFromHref (tile-link awareness); tests pass a stub.
// Keeping it a callback avoids an import cycle (client/embed imports
// client/markdown).
type ClassifyFunc func(sp Span) AtomKind

// Layout turns a lowered document into positioned draw ops at the given content
// width. It is pure: all canvas interaction is deferred to the painter, which
// just walks LayoutResult.Ops and scales them. width is the content width in
// logical px (the area between the left pad and the right edge).
func Layout(doc Node, m Measure, classify ClassifyFunc, width float64, style LayoutStyle) LayoutResult {
	lw := &layoutWriter{m: m, classify: classify, style: style}
	y := lw.blocks(doc.Children, style.PadX, 0, width-2*style.PadX, 0)
	return LayoutResult{Ops: lw.ops, Height: y}
}

// layoutWriter accumulates ops while walking the block tree.
type layoutWriter struct {
	m        Measure
	classify ClassifyFunc
	style    LayoutStyle
	ops      []DrawOp
}

// atomKind classifies a span, tolerating a nil classifier (tests / no embeds).
func (w *layoutWriter) atomKind(sp Span) AtomKind {
	if w.classify == nil {
		return AtomNone
	}
	return w.classify(sp)
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
	case NodeTable:
		return w.table(n, x, y, avail)
	}
	return y
}

// codeBlock draws a background panel and each raw line as monospace text.
func (w *layoutWriter) codeBlock(n Node, x, y, avail float64) float64 {
	body := ""
	if len(n.Spans) > 0 {
		body = n.Spans[0].Text
	}
	fp := w.style.CodeFontPx
	lh := fp * w.style.LineSpacing
	nlines := strings.Count(body, "\n") + 1
	height := w.style.CodePadY*2 + lh*float64(nlines)
	w.ops = append(w.ops, DrawOp{Kind: OpRect, X: x, Y: y, W: avail, H: height, Color: ColorCodeBg})

	// Syntax-highlight into colored runs; split each run on newlines so a
	// multi-line string/comment keeps its color across lines. Highlighting is
	// a partition of the body (tokens concatenate back to it), so tracking
	// (line, col) across the parts recovers each part's position in the body —
	// and n.LineStarts turns that into a source offset: every emitted run is a
	// verbatim source slice, giving the rendered-mode caret entry into code.
	cx := x + w.style.CodePadX
	ty := y + w.style.CodePadY
	line, col := 0, 0
	for _, tk := range highlight(body, n.Lang) {
		for pi, part := range strings.Split(tk.Text, "\n") {
			if pi > 0 {
				ty += lh
				cx = x + w.style.CodePadX
				line++
				col = 0
			}
			if part == "" {
				continue
			}
			op := DrawOp{Kind: OpText, X: cx, Y: ty, CodeBlock: true,
				Text: part, FontPx: fp, Style: StyleCode, Mono: true, Color: tk.Color}
			if line < len(n.LineStarts) {
				op.SrcStart, op.SrcLen = n.LineStarts[line]+col, len(part)
			}
			w.ops = append(w.ops, op)
			cx += w.m(part, fp, StyleCode, true)
			col += len(part)
		}
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

// table lays out a GFM table: content-sized columns fit to avail, per-row
// height from wrapped cells, per-column alignment, a bold/tinted header row,
// and a full gridline grid. Returns the y past the table.
func (w *layoutWriter) table(n Node, x, y, avail float64) float64 {
	rows := n.Children
	if len(rows) == 0 {
		return y
	}
	ncol := len(n.Align)
	for _, r := range rows {
		if len(r.Children) > ncol {
			ncol = len(r.Children)
		}
	}
	if ncol == 0 {
		return y
	}
	fp := w.style.BaseFontPx
	lineH := fp * w.style.LineSpacing
	padX, padY := w.style.TablePadX, w.style.TablePadY

	// Natural (single-line) and minimum (widest unbreakable token) column
	// widths, including cell padding.
	natW := make([]float64, ncol)
	minW := make([]float64, ncol)
	for _, row := range rows {
		for ci := 0; ci < ncol && ci < len(row.Children); ci++ {
			toks := w.tokenize(row.Children[ci].Spans, fp, false)
			if nat := lineWidth(toks) + 2*padX; nat > natW[ci] {
				natW[ci] = nat
			}
			if mn := widestToken(toks) + 2*padX; mn > minW[ci] {
				minW[ci] = mn
			}
		}
	}
	colW := distributeColumns(natW, minW, avail)

	colX := make([]float64, ncol+1)
	colX[0] = x
	for ci := 0; ci < ncol; ci++ {
		colX[ci+1] = colX[ci] + colW[ci]
	}
	tableW := colX[ncol] - x

	grid := ColorTableGrid
	topY := y
	w.ops = append(w.ops, DrawOp{Kind: OpRect, X: x, Y: y, W: tableW, H: 1, Color: grid}) // top border

	for ri, row := range rows {
		header := ri == 0
		rowTop := y

		cellLines := make([][][]inlineToken, ncol)
		rowH := lineH + 2*padY
		for ci := 0; ci < ncol; ci++ {
			var spans []Span
			if ci < len(row.Children) {
				spans = row.Children[ci].Spans
			}
			lines := wrapTokens(w.tokenize(spans, fp, false), colW[ci]-2*padX)
			cellLines[ci] = lines
			if ch := float64(maxInt(len(lines), 1))*lineH + 2*padY; ch > rowH {
				rowH = ch
			}
		}
		if header {
			w.ops = append(w.ops, DrawOp{Kind: OpRect, X: x, Y: rowTop, W: tableW, H: rowH, Color: ColorTableHeaderBg})
		}
		for ci := 0; ci < ncol; ci++ {
			align := AlignLeft
			if ci < len(n.Align) && n.Align[ci] != AlignNone {
				align = n.Align[ci]
			}
			contentW := colW[ci] - 2*padX
			cy := rowTop + padY
			for _, line := range cellLines[ci] {
				if header {
					for i := range line {
						line[i].span.Style |= StyleBold
					}
				}
				lx := colX[ci] + padX
				switch align {
				case AlignCenter:
					lx += (contentW - lineWidth(line)) / 2
				case AlignRight:
					lx += contentW - lineWidth(line)
				}
				w.emitLine(line, lx, cy, lineH, fp, ColorText, false)
				cy += lineH
			}
		}
		y = rowTop + rowH
		w.ops = append(w.ops, DrawOp{Kind: OpRect, X: x, Y: y, W: tableW, H: 1, Color: grid}) // row separator
	}
	for ci := 0; ci <= ncol; ci++ {
		w.ops = append(w.ops, DrawOp{Kind: OpRect, X: colX[ci], Y: topY, W: 1, H: y - topY, Color: grid})
	}
	return y
}

// distributeColumns fits natural column widths to avail. When they already fit,
// they're kept as-is; when they overflow, columns shrink proportionally to
// their slack (natural − min), never below min (a too-wide table then overflows
// and is clipped at the tile rect rather than mangling content).
func distributeColumns(natW, minW []float64, avail float64) []float64 {
	out := make([]float64, len(natW))
	copy(out, natW)
	var total float64
	for _, v := range natW {
		total += v
	}
	if total <= avail || total == 0 {
		return out
	}
	var slack float64
	for i := range out {
		slack += out[i] - minW[i]
	}
	if slack <= 0 {
		copy(out, minW)
		return out
	}
	remove := total - avail
	if remove > slack {
		remove = slack
	}
	for i := range out {
		out[i] -= (out[i] - minW[i]) / slack * remove
	}
	return out
}

func widestToken(toks []inlineToken) float64 {
	var m float64
	for _, tk := range toks {
		if !tk.isSpace && !tk.isBreak && tk.width > m {
			m = tk.width
		}
	}
	return m
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// inline wraps a block's spans into lines within avail, emitting OpText (and
// atomic OpEmbed) ops left-aligned at x, and returns the y past the last line.
func (w *layoutWriter) inline(spans []Span, x, y, avail, fontPx float64, color ColorRole, mono bool) float64 {
	lineH := fontPx * w.style.LineSpacing
	for _, line := range wrapTokens(w.tokenize(spans, fontPx, mono), avail) {
		h := lineHeight(line, lineH)
		w.emitLine(line, x, y, h, fontPx, color, mono)
		y += h
	}
	return y
}

// wrapTokens greedily breaks a token stream into lines fitting avail. Leading
// whitespace on each line is dropped; a break token forces a new line.
func wrapTokens(tokens []inlineToken, avail float64) [][]inlineToken {
	var lines [][]inlineToken
	var line []inlineToken
	var lineW float64
	for _, tk := range tokens {
		if tk.isBreak {
			lines = append(lines, line) // may be nil (a deliberate blank line)
			line, lineW = nil, 0
			continue
		}
		if !tk.isSpace && lineW+tk.width > avail && len(line) > 0 {
			lines = append(lines, line)
			line, lineW = nil, 0
		}
		if tk.isSpace && len(line) == 0 {
			continue
		}
		line = append(line, tk)
		lineW += tk.width
	}
	if len(line) > 0 {
		lines = append(lines, line)
	}
	return lines
}

// lineHeight returns a line's height: the text line height, grown to fit any
// atomic embed on the line.
func lineHeight(line []inlineToken, textLineH float64) float64 {
	h := textLineH
	for _, tk := range line {
		if tk.atomic && tk.height > h {
			h = tk.height
		}
	}
	return h
}

// lineWidth returns the total advance width of a line's tokens (for alignment).
func lineWidth(line []inlineToken) float64 {
	var sum float64
	for _, tk := range line {
		sum += tk.width
	}
	return sum
}

// emitLine paints one wrapped line's tokens starting at (x, y), with the given
// line height h (used for the inline-code chip background).
func (w *layoutWriter) emitLine(line []inlineToken, x, y, h, fontPx float64, color ColorRole, mono bool) {
	cx := x
	for _, tk := range line {
		if tk.atomic {
			alt := tk.span.Alt
			if alt == "" {
				alt = tk.span.Text
			}
			if tk.isImage {
				w.ops = append(w.ops, DrawOp{Kind: OpImage, X: cx, Y: y, W: tk.width, H: tk.height,
					Src: tk.span.Src, Alt: alt})
			} else {
				w.ops = append(w.ops, DrawOp{Kind: OpEmbed, X: cx, Y: y, W: tk.width, H: tk.height,
					Href: tk.span.Href, Src: tk.span.Src, Alt: alt})
			}
			cx += tk.width
			continue
		}
		col := color
		if tk.span.Style&StyleLink != 0 {
			col = ColorLink
		} else if tk.span.Style&StyleCode != 0 {
			col = ColorCode
			w.ops = append(w.ops, DrawOp{Kind: OpRect, X: cx - 2, Y: y, W: tk.width + 4, H: h, Color: ColorInlineCodeBg})
		}
		w.ops = append(w.ops, DrawOp{Kind: OpText, X: cx, Y: y, Text: tk.span.Text,
			FontPx: fontPx, Style: tk.span.Style, Mono: mono || tk.span.Style&StyleCode != 0,
			Color: col, Href: tk.span.Href,
			SrcStart: tk.span.SrcStart, SrcLen: tk.span.SrcLen})
		cx += tk.width
	}
}

// inlineToken is one atomic unit of inline flow: a word, a space run, or an
// atomic embed/image block.
type inlineToken struct {
	span    Span
	width   float64
	isSpace bool
	isBreak bool // a hard line break ("\n" sentinel)
	atomic  bool
	isImage bool // atomic && a real image (OpImage) vs a tile embed (OpEmbed)
	height  float64
}

// tokenize splits spans into flow tokens: text spans break on whitespace
// boundaries (so wrapping can happen between words); embed spans pass through
// as a single atomic block sized by the style defaults or the span's W/H.
func (w *layoutWriter) tokenize(spans []Span, fontPx float64, mono bool) []inlineToken {
	var toks []inlineToken
	for _, sp := range spans {
		if kind := w.atomKind(sp); kind != AtomNone {
			// Coalesce consecutive tile-embed spans that share the same
			// non-empty href into one embed: a tile link whose text is several
			// inline spans (e.g. "[a **b**](/5)") is ONE embed, not several.
			if kind == AtomEmbed && sp.Href != "" && len(toks) > 0 {
				if last := &toks[len(toks)-1]; last.atomic && !last.isImage && last.span.Href == sp.Href {
					last.span.Text += sp.Text
					continue
				}
			}
			ew, eh := w.embedSize(sp)
			toks = append(toks, inlineToken{span: sp, width: ew, height: eh, atomic: true, isImage: kind == AtomImage})
			continue
		}
		if sp.Text == "\n" {
			// Hard-break sentinel from lowering.
			toks = append(toks, inlineToken{isBreak: true})
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
			// Carry the source range for the caret. A verbatim span (SrcLen ==
			// len(Text)) maps byte-for-byte, so this sub-run starts at SrcStart+i
			// and spans j-i source bytes. A non-verbatim span (inline code, an
			// autolink) is opaque: SrcLen 0 so the caret skips it.
			if sp.SrcLen == len(sp.Text) {
				tok.SrcStart = sp.SrcStart + i
				tok.SrcLen = j - i
			} else {
				tok.SrcLen = 0
			}
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
