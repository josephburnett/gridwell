//go:build js && wasm

package main

import (
	"fmt"
	"hash/fnv"
	"strings"
	"syscall/js"

	embedpkg "github.com/josephburnett/gridwell/client/embed"
	"github.com/josephburnett/gridwell/client/markdown"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// This file holds all markdown rendering for the canvas: the App-method entry
// points (preview vs live-pane) and the painter that walks the pure layout
// (client/markdown: parse → lower → layout → []DrawOp) into canvas draw calls.
// All layout lives in client/markdown; this file only paints + scales the ops
// and dispatches embeds.

// drawMarkdownInPane renders a markdown file as the contents of the pane
// currently descended into it, using that pane's live TextMode/TextScroll so
// split panes scroll independently. In text mode the focused pane is skipped
// (its textarea overlay handles it); other panes still render canvas text.
func (a *App) drawMarkdownInPane(p *pane.Pane, n *rpc.Tile, x, y, w, h float64) {
	mode := p.TextMode
	if mode == "" {
		mode = rpc.TextModeRendered
	}
	scale := fileFixedScale
	scrollX := p.TextScrollX
	scrollY := p.TextScrollY

	a.cctx.Call("save")
	a.cctx.Call("beginPath")
	a.cctx.Call("rect", x, y, w, h)
	a.cctx.Call("clip")

	hideForTextarea := mode == rpc.TextModeText && p.ID == a.tree.Focus
	if !hideForTextarea {
		if body, ok := a.tileBody(n); ok {
			a.drawMarkdownInRect(string(body),
				x-scrollX*scale, y-scrollY*scale,
				fileNaturalContentPx*scale, h+scrollY*scale,
				scale, mode, a.makeEmbedDrawer(p.ID))
		}
	} else {
		a.tileBody(n) // warm the cache so the textarea has content when shown
	}

	a.cctx.Call("restore")
}

// drawMarkdownNode renders a markdown file tile at (x, y, w, h) — the preview
// (no pane descended) or the live cover-crop while a pane is descended. The
// scale/scroll selection is unchanged from before; only the inner paint moved
// to the new pipeline.
func (a *App) drawMarkdownNode(n *rpc.Tile, x, y, w, h float64, _ pane.Rect, selected, outside, dashed bool) {
	mode := n.TextMode
	if mode == "" {
		mode = rpc.TextModeText
	}
	fp := a.paneFocusedOnFile(n.ID)
	// Scale/scroll selection (focused inner-box cover-crop / stored framing /
	// natural-width fallback) is the pure markdown.PreviewScaleScroll — the
	// "preview cover-crops like the live pane" (preview = descent) math.
	var iw, ih, fScrollX, fScrollY float64
	focused := fp != nil
	if focused {
		if fp.TextMode != "" {
			mode = fp.TextMode
		}
		fpRect := a.paneRectByID(fp.ID)
		_, _, iw, ih = fileInnerBox(fp, fpRect)
		fScrollX = fp.TextScrollX
		fScrollY = fp.TextScrollY
	}
	frame := markdown.PreviewScaleScroll(w, h, focused, iw, ih, fScrollX, fScrollY,
		n.TextW, n.TextH, n.TextX, n.TextY, fileNaturalContentPx, fileFixedScale, 0.02)
	scale, scrollX, scrollY := frame.Scale, frame.ScrollX, frame.ScrollY

	a.cctx.Call("save")
	a.cctx.Call("beginPath")
	a.cctx.Call("rect", x, y, w, h)
	a.cctx.Call("clip")

	a.cctx.Set("fillStyle", colorFileInnerBg)
	a.cctx.Call("fillRect", x, y, w, h)

	hideForTextarea := fp != nil && fp.TextMode == rpc.TextModeText && fp.ID == a.tree.Focus
	if !hideForTextarea {
		if body, ok := a.tileBody(n); ok {
			a.drawMarkdownInRect(string(body),
				x-scrollX*scale, y-scrollY*scale,
				fileNaturalContentPx*scale, h+scrollY*scale,
				scale, mode, a.makePreviewEmbedDrawer(uuidOf(n.GridID)))
		}
	} else {
		a.tileBody(n) // warm the cache so the textarea has content when shown
	}

	a.cctx.Call("restore")

	outlineColor := colorMarkdownLine
	if outside {
		outlineColor = colorPluginBorder
	}
	if dashed {
		setTileDash(a.cctx)
	}
	strokeTileBorder(a.cctx, x, y, w, h, outlineColor, tileBorderPx)
	if dashed {
		clearTileDash(a.cctx)
	}
	if selected {
		drawSelectedTileOutline(a.cctx, x, y, w, h)
	}
}

// drawMarkdownInRect lays out and paints `src` into (x, y, w, h) at `scale`.
// The rendered branch uses the new layout pipeline; text mode stays raw
// monospace. The rect's clip is the caller's responsibility; (x, y) is the
// already-scroll-offset drawing origin.
func (a *App) drawMarkdownInRect(src string, x, y, w, h, scale float64, mode string, drawEmbed embedDrawer) {
	if mode == rpc.TextModeText {
		drawMarkdownText(a.cctx, src, x, y, w, h, scale, 0)
		return
	}
	a.drawMarkdownRendered(src, x, y, w, h, scale, drawEmbed)
}

// markdownStyle holds the per-block font/spacing/color parameters in logical
// pixels (before scale). It is the single source the LayoutStyle and the
// color map are derived from.
type markdownStyle struct {
	bodyPx     float64
	h1Px       float64
	h2Px       float64
	h3Px       float64
	codePx     float64
	pad        float64
	gapAfter   float64
	monospace  string
	sansSerif  string
	textColor  string
	mutedColor string
	codeBg     string
	quoteBar   string
}

func defaultMarkdownStyle() markdownStyle {
	return markdownStyle{
		bodyPx:     14,
		h1Px:       24,
		h2Px:       19,
		h3Px:       16,
		codePx:     13,
		pad:        6,
		gapAfter:   4,
		monospace:  `ui-monospace, "SF Mono", Menlo, Consolas, monospace`,
		sansSerif:  `ui-sans-serif, system-ui, -apple-system, sans-serif`,
		textColor:  "#d8d9de",
		mutedColor: "#9ca0ad",
		codeBg:     "#1c1d24",
		quoteBar:   "#3a4b5a",
	}
}

// markdownLayoutStyle maps the renderer's style to the pure LayoutStyle the
// layout pass consumes.
func markdownLayoutStyle(st markdownStyle) markdown.LayoutStyle {
	return markdown.LayoutStyle{
		BaseFontPx:  st.bodyPx,
		HeadingPx:   [7]float64{0, st.h1Px, st.h2Px, st.h3Px, st.bodyPx * 1.15, st.bodyPx, st.bodyPx * 0.95},
		CodeFontPx:  st.codePx,
		LineSpacing: 1.35,
		BlockGap:    st.bodyPx * 0.7,
		PadX:        st.pad,
		ListIndent:  16,
		QuoteIndent: 12,
		QuoteBarW:   3,
		CodePadX:    4,
		CodePadY:    2,
		TablePadX:   5,
		TablePadY:   3,
		EmbedW:      defaultEmbedW,
		EmbedH:      defaultEmbedH,
	}
}

// markdownColorFor resolves a layout ColorRole to a concrete CSS color.
func markdownColorFor(st markdownStyle, role markdown.ColorRole) string {
	switch role {
	case markdown.ColorLink:
		return colorURLLine
	case markdown.ColorMuted, markdown.ColorRuleLine, markdown.ColorTableGrid:
		return st.mutedColor
	case markdown.ColorCodeBg, markdown.ColorTableHeaderBg:
		return st.codeBg
	case markdown.ColorInlineCodeBg:
		return colorMarkdownCodeBg2
	case markdown.ColorQuoteBar:
		return st.quoteBar
	case markdown.ColorSynKeyword:
		return "#c678dd" // purple
	case markdown.ColorSynString:
		return "#98c379" // green
	case markdown.ColorSynComment:
		return "#7f848e" // grey
	case markdown.ColorSynNumber:
		return "#d19a66" // orange
	case markdown.ColorSynType:
		return "#56b6c2" // cyan
	case markdown.ColorSynFunction:
		return "#61afef" // blue
	}
	// ColorText / ColorHeading / ColorCode.
	return st.textColor
}

// drawMarkdownRendered lays out `src` via the pure pipeline and paints the
// resulting draw ops scaled by `scale` from origin (x, y). h is the available
// (scroll-adjusted) content height used to cull below-the-fold ops.
func (a *App) drawMarkdownRendered(src string, x, y, w, h, scale float64, drawEmbed embedDrawer) {
	st := defaultMarkdownStyle()
	lstyle := markdownLayoutStyle(st)
	c := a.cctx

	contentWidthLogical := w / scale
	if contentWidthLogical < 16 {
		contentWidthLogical = 16
	}

	measure := func(text string, fontPx float64, style markdown.SpanStyle, mono bool) float64 {
		family := st.sansSerif
		if mono {
			family = st.monospace
		}
		setFont(c, fontPx*scale, family, style&markdown.StyleBold != 0, style&markdown.StyleItalic != 0)
		return c.Call("measureText", text).Get("width").Float() / scale
	}

	res := a.layoutMarkdown(src, contentWidthLogical, measure, lstyle)

	c.Set("textBaseline", "top")
	for i := range res.Ops {
		op := &res.Ops[i]
		// Below-the-fold cull (clip handles correctness; this skips work).
		if op.Y*scale > h {
			continue
		}
		sx := x + op.X*scale
		sy := y + op.Y*scale
		switch op.Kind {
		case markdown.OpRect:
			c.Set("fillStyle", markdownColorFor(st, op.Color))
			c.Call("fillRect", sx, sy, op.W*scale, op.H*scale)
		case markdown.OpRule:
			lh := op.H * scale
			if lh < 1 {
				lh = 1
			}
			c.Set("fillStyle", markdownColorFor(st, op.Color))
			c.Call("fillRect", sx, sy, op.W*scale, lh)
		case markdown.OpImage:
			a.drawMarkdownImage(c, op.Src, op.Alt, sx, sy, op.W*scale, op.H*scale, scale, st)
		case markdown.OpEmbed:
			ew := op.W * scale
			eh := op.H * scale
			if drawEmbed != nil {
				drawEmbed(sx, sy, ew, eh, op.Href, op.Alt)
			} else {
				setFont(c, st.bodyPx*scale, st.monospace, false, true)
				c.Set("fillStyle", st.mutedColor)
				label := op.Alt
				if label == "" {
					label = "[embed]"
				}
				c.Call("fillText", label, sx, sy)
			}
		case markdown.OpText:
			family := st.sansSerif
			if op.Mono {
				family = st.monospace
			}
			setFont(c, op.FontPx*scale, family,
				op.Style&markdown.StyleBold != 0, op.Style&markdown.StyleItalic != 0)
			c.Set("fillStyle", markdownColorFor(st, op.Color))
			c.Call("fillText", op.Text, sx, sy)
			if op.Style&(markdown.StyleLink|markdown.StyleStrike) != 0 {
				wpx := c.Call("measureText", op.Text).Get("width").Float()
				if op.Style&markdown.StyleLink != 0 {
					c.Call("fillRect", sx, sy+op.FontPx*scale*1.15, wpx, 1)
				}
				if op.Style&markdown.StyleStrike != 0 {
					c.Call("fillRect", sx, sy+op.FontPx*scale*0.55, wpx, 1)
				}
			}
		}
	}
}

// mdCacheKey identifies a memoized layout: a content hash plus the rounded
// content width. The measure is deterministic for the renderer's fixed fonts,
// so a layout is valid to reuse across frames and zoom levels (positions are
// logical and scaled only at paint time).
type mdCacheKey struct {
	hash  uint64
	width int64
}

// layoutMarkdown returns the layout for (src, width), computing and caching it
// on a miss. The cache is bounded; on overflow it's cleared wholesale (text
// docs in view are few, so a simple cap beats LRU bookkeeping).
func (a *App) layoutMarkdown(src string, width float64, m markdown.Measure, style markdown.LayoutStyle) markdown.LayoutResult {
	if a.mdCache == nil {
		a.mdCache = map[mdCacheKey]markdown.LayoutResult{}
	}
	hsh := fnv.New64a()
	hsh.Write([]byte(src))
	key := mdCacheKey{hash: hsh.Sum64(), width: int64(width)}
	if r, ok := a.mdCache[key]; ok {
		return r
	}
	if len(a.mdCache) > 128 {
		a.mdCache = map[mdCacheKey]markdown.LayoutResult{}
	}
	r := markdown.Layout(markdown.Lower([]byte(src)), m, classifyAtom, width, style)
	a.mdCache[key] = r
	return r
}

// classifyAtom splits an inline span into a native tile embed (a tile-path
// href, including the legacy image-in-link form), a real external image, or
// flowing text — see markdown.ClassifyFunc.
func classifyAtom(sp markdown.Span) markdown.AtomKind {
	if embedpkg.LeafTileIDFromHref(sp.Href) != "" {
		return markdown.AtomEmbed
	}
	if sp.Style&markdown.StyleEmbed != 0 {
		return markdown.AtomImage
	}
	return markdown.AtomNone
}

// drawMarkdownImage paints a real markdown image (![alt](src)) into the box
// (x, y, w, h). It lazily loads the image element, draws it contained (aspect
// preserved) once ready, and shows an alt-text placeholder while loading or on
// error. The browser caches the URL fetch; we cache the decoded element.
func (a *App) drawMarkdownImage(c js.Value, src, alt string, x, y, w, h, scale float64, st markdownStyle) {
	if a.mdImages == nil {
		a.mdImages = map[string]js.Value{}
		a.mdImageState = map[string]int8{}
	}
	switch a.mdImageState[src] {
	case 1: // ready
		img := a.mdImages[src]
		iw := img.Get("naturalWidth").Float()
		ih := img.Get("naturalHeight").Float()
		dx, dy, dw, dh := containRect(x, y, w, h, iw, ih)
		c.Call("drawImage", img, dx, dy, dw, dh)
	case 2: // error
		drawImagePlaceholder(c, alt, x, y, w, h, scale, st, true)
	default: // loading / not yet started
		if _, ok := a.mdImages[src]; !ok {
			a.startMarkdownImageLoad(src)
		}
		drawImagePlaceholder(c, alt, x, y, w, h, scale, st, false)
	}
}

// startMarkdownImageLoad kicks off an async <img> load for src, redrawing when
// it resolves. Callbacks are released on completion.
func (a *App) startMarkdownImageLoad(src string) {
	img := js.Global().Get("Image").New()
	a.mdImages[src] = img
	a.mdImageState[src] = 0
	var onload, onerror js.Func
	finish := func(state int8) {
		a.mdImageState[src] = state
		onload.Release()
		onerror.Release()
		a.draw()
	}
	onload = js.FuncOf(func(js.Value, []js.Value) any { finish(1); return nil })
	onerror = js.FuncOf(func(js.Value, []js.Value) any { finish(2); return nil })
	img.Set("onload", onload)
	img.Set("onerror", onerror)
	img.Set("src", src)
}

// containRect fits an (iw × ih) image inside (x, y, w, h), preserving aspect
// ratio and centering. Degenerate intrinsic sizes fall back to filling the box.
func containRect(x, y, w, h, iw, ih float64) (dx, dy, dw, dh float64) {
	if iw <= 0 || ih <= 0 || w <= 0 || h <= 0 {
		return x, y, w, h
	}
	s := w / iw
	if sy := h / ih; sy < s {
		s = sy
	}
	dw, dh = iw*s, ih*s
	return x + (w-dw)/2, y + (h-dh)/2, dw, dh
}

// drawImagePlaceholder draws a bordered box with the alt label, used while a
// markdown image loads or after it fails.
func drawImagePlaceholder(c js.Value, alt string, x, y, w, h, scale float64, st markdownStyle, isError bool) {
	c.Set("fillStyle", st.codeBg)
	c.Call("fillRect", x, y, w, h)
	c.Set("strokeStyle", st.mutedColor)
	c.Set("lineWidth", 1)
	c.Call("strokeRect", x+0.5, y+0.5, w-1, h-1)
	label := alt
	if label == "" {
		label = "image"
	}
	if isError {
		label = "⚠ " + label
	}
	setFont(c, st.bodyPx*scale*0.9, st.sansSerif, false, false)
	c.Set("fillStyle", st.mutedColor)
	c.Set("textBaseline", "top")
	c.Call("fillText", label, x+4, y+4)
}

// setFont assembles a CSS font shorthand and assigns it. size is in pixels.
func setFont(c js.Value, sizePx float64, family string, bold, italic bool) {
	style := "normal"
	if italic {
		style = "italic"
	}
	weight := "normal"
	if bold {
		weight = "bold"
	}
	if sizePx < 1 {
		sizePx = 1
	}
	c.Set("font", fmt.Sprintf("%s %s %.2fpx %s", style, weight, sizePx, family))
}

// drawMarkdownText paints src as raw monospace text at the given scale. Used
// for source-mode preview and as a faint backdrop behind the textarea overlay.
// Text mode does not soft-wrap; long lines are clipped by the caller's clip.
// rawTextLineHeight is the line-advance multiple for raw monospace markdown
// source. Shared by the canvas painter (drawMarkdownText) and the editing
// <textarea> (file_overlay.go) so a focused pane's textarea and its blurred
// canvas preview render line-for-line identically — the same content, the
// same size, the same place, whether or not the pane has focus.
const rawTextLineHeight = 1.35

func drawMarkdownText(c js.Value, src string, x, y, _ /* w */, h, scale, scrollY float64) {
	st := defaultMarkdownStyle()
	fontPx := st.codePx
	setFont(c, fontPx*scale, st.monospace, false, false)
	c.Set("fillStyle", st.textColor)
	// Place each line's baseline exactly where a CSS line box would, so this
	// matches the editing <textarea> (file_overlay.go) to the pixel and the
	// raw text doesn't shift when focus enters or leaves the pane. A line box
	// is lineHeight tall; the font's content area (fontBoundingBox asc+desc,
	// for whatever font Chromium actually resolved) is centered in it with
	// equal leading top and bottom, and the alphabetic baseline sits one
	// ascent below the content-area top. measureText reports asc/desc in the
	// current (already-scaled) font, so the slot height is scaled to match.
	c.Set("textBaseline", "alphabetic")
	m := c.Call("measureText", "M")
	asc := m.Get("fontBoundingBoxAscent").Float()
	desc := m.Get("fontBoundingBoxDescent").Float()
	// Slot/baseline/top math is the pure markdown.RawTextLineSlot — the
	// pixel-match-the-textarea contract. asc/desc come from the scaled canvas
	// font above.
	slotted := markdown.RawTextLineSlot(fontPx, rawTextLineHeight, scale, st.pad, scrollY, asc, desc)
	slotTop := slotted.Top0
	for ln := range strings.SplitSeq(src, "\n") {
		if markdown.RawTextLineVisible(slotTop, slotted.Slot, h) {
			c.Call("fillText", ln, x+st.pad*scale, y+slotTop+slotted.Baseline)
		}
		slotTop += slotted.Slot
	}
}
