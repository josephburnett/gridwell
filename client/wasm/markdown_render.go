//go:build js && wasm

package main

import (
	"fmt"
	"hash/fnv"
	"strings"
	"syscall/js"

	embedpkg "github.com/josephburnett/gridwell/client/embed"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/markdown"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/textedit"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// This file holds all markdown rendering for the canvas: the App-method entry
// points (preview vs live-pane) and the painter that walks the pure layout
// (client/markdown: parse → lower → layout → []DrawOp) into canvas draw calls.
// All layout lives in client/markdown; this file only paints + scales the ops
// and dispatches embeds.

// textContentWidth is the logical width rendered markdown wraps at for pane p:
// the pane's inner reading-box width. Using the pane's own width (not a fixed
// 800px constant) is what reflows the doc to the pane — a split pane is narrower
// than 800px, so the old fixed width laid the doc out at 800 and the pane clip
// chopped the right edge ("cut off"). The painter, both caret hit-tests, and
// caret movement all read this one width so they stay in agreement; the preview
// lays out at the framing width it cover-crops (PreviewFrame.ContentW), which
// for a focused tile is this same value — so an unfocused pane is a true scaled
// copy of the focused one, not a re-wrap. (Scale is fixed at 1.0 in a descended
// file pane, so screen px and logical px coincide here.)
func (a *App) textContentWidth(p *pane.Pane) float64 {
	_, _, w, _ := textInnerBox(p, paneRectFor(a, p))
	return w
}

// drawMarkdownInPane renders a markdown file as the contents of the pane
// currently descended into it, using that pane's live TextMode/TextScroll so
// split panes scroll independently. In text mode the focused pane is skipped
// (its textarea overlay handles it); other panes still render canvas text.
func (a *App) drawMarkdownInPane(p *pane.Pane, n *rpc.Tile, x, y, w, h float64) {
	mode := p.TextMode
	if mode == "" {
		mode = rpc.TextModeRendered
	}
	// (x, y) is the inner box top-left (textInnerBox); markdownOrigin(p, r)
	// rederives the same point for the caret hit-test, so they stay in sync.
	scale := textFixedScale
	originX := x - p.TextScrollX*scale
	originY := y - p.TextScrollY*scale

	a.cctx.Call("save")
	a.cctx.Call("beginPath")
	a.cctx.Call("rect", x, y, w, h)
	a.cctx.Call("clip")

	// CanvasHiddenByOverlay is the single owner of the "canvas paints vs overlay
	// covers" decision. It returns true only when all four hold: this is a
	// descended pane (not a preview), it is the tree's focused pane (the one the
	// textarea is over), the tile is in text mode (the textarea is only shown in
	// text mode), AND the textarea currently has content (false during the loading
	// race on a pane switch — canvas must paint until the overlay has actual text).
	hideForTextarea := textedit.CanvasHiddenByOverlay(
		true,                     // descended pane (not a preview node)
		p.ID == a.tree.Focus,     // the pane the textarea is positioned over
		mode == rpc.TextModeText, // textarea is only shown in text mode
		a.textareaReady,          // textarea actually has content (not loading)
	)
	if !hideForTextarea {
		if body, ok := a.tileBody(n); ok {
			a.drawMarkdownInRect(string(body),
				originX, originY,
				a.textContentWidth(p), h+p.TextScrollY*scale,
				scale, mode, a.makeEmbedDrawer(p.ID))
			// The editing caret rides on the focused, rendered pane only — same
			// origin/scale (and width) the markdown was painted with.
			if mode == rpc.TextModeRendered && p.ID == a.tree.Focus {
				a.drawMarkdownCaret(p, string(body), originX, originY, scale)
			}
		}
	} else {
		a.tileBody(n) // warm the cache so the textarea has content when shown
	}

	a.cctx.Call("restore")
}

// drawMarkdownNode renders a markdown file tile at (x, y, w, h) as a preview.
// The scale/scroll comes from the tile's own stored framing (TextW/TextH/TextX/
// TextY) — never from another pane's live state. Per the guiding rule: preview
// = descent target = ascent return; the stored framing IS the preview. Before
// fix #35 this called paneFocusedOnFile and used the other pane's live inner
// width (focused=true), causing two bugs: wrong-size preview (A) because the
// layout width tracked the sibling pane's width, and blank preview (B) because
// hideForTextarea suppressed canvas paint for every preview of a tile being
// edited elsewhere.
func (a *App) drawMarkdownNode(n *rpc.Tile, x, y, w, h float64, _ pane.Rect, selected, outside, dashed bool) {
	mode := n.TextMode
	if mode == "" {
		mode = rpc.TextModeText
	}
	// Always pass focused=false: the preview uses the tile's own stored framing.
	// Never reach into another pane's live width/scroll (paneFocusedOnFile).
	frame := markdown.PreviewScaleScroll(w, h, false, 0, 0, 0, 0,
		n.TextW, n.TextH, n.TextX, n.TextY, textNaturalContentPx, textFixedScale, 0.02)
	scale, scrollX, scrollY := frame.Scale, frame.ScrollX, frame.ScrollY

	a.cctx.Call("save")
	a.cctx.Call("beginPath")
	a.cctx.Call("rect", x, y, w, h)
	a.cctx.Call("clip")

	a.cctx.Set("fillStyle", colorFileInnerBg)
	a.cctx.Call("fillRect", x, y, w, h)

	// No hideForTextarea in the preview path: the single textarea overlay covers
	// only the focused descended pane, never a preview node. Suppressing canvas
	// here caused blank previews when another pane was editing in text mode (Bug B).
	if body, ok := a.tileBody(n); ok {
		// Lay out at the framing width the cover-crop was computed against
		// (frame.ContentW = stored TextW / natural fallback) and merely SCALE by
		// frame.Scale, so the preview is a true scaled copy of what the descended
		// pane showed at its last ascent — never re-wrapped to the tile's footprint.
		a.drawMarkdownInRect(string(body),
			x-scrollX*scale, y-scrollY*scale,
			frame.ContentW*scale, h+scrollY*scale,
			scale, mode, a.makePreviewEmbedDrawer(uuidOf(n.GridID)))
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

	measure := a.markdownMeasure(st, scale)
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

// markdownMeasure builds the layout Measure backed by the canvas measureText,
// at the given render scale. Shared by the painter and the caret hit-test so
// click→offset and offset→screen agree with what was painted.
func (a *App) markdownMeasure(st markdownStyle, scale float64) markdown.Measure {
	c := a.cctx
	return func(text string, fontPx float64, style markdown.SpanStyle, mono bool) float64 {
		family := st.sansSerif
		if mono {
			family = st.monospace
		}
		setFont(c, fontPx*scale, family, style&markdown.StyleBold != 0, style&markdown.StyleItalic != 0)
		return c.Call("measureText", text).Get("width").Float() / scale
	}
}

// markdownOrigin is the drawing origin (top-left of logical content, already
// scrolled) and render scale for the markdown of a descended pane — the single
// source of truth the painter and the caret hit-test both transform through.
func (a *App) markdownOrigin(p *pane.Pane, r pane.Rect) (originX, originY, scale float64) {
	x, y, _, _ := textInnerBox(p, r)
	scale = textFixedScale
	return x - p.TextScrollX*scale, y - p.TextScrollY*scale, scale
}

// markdownCaretAt maps a screen point in a descended markdown pane to the
// nearest source byte offset, via the same layout + transform the painter used.
// ok is false when the tile has no cached body or no text to land on.
func (a *App) markdownCaretAt(p *pane.Pane, r pane.Rect, n *rpc.Tile, sx, sy float64) (int, bool) {
	body, ok := a.tileBody(n)
	if !ok {
		return 0, false
	}
	st := defaultMarkdownStyle()
	measure := a.markdownMeasure(st, textFixedScale)
	res := a.layoutMarkdown(string(body), a.textContentWidth(p), measure, markdownLayoutStyle(st))
	originX, originY, scale := a.markdownOrigin(p, r)
	return markdown.CaretFromPoint(res.Ops, string(body), (sx-originX)/scale, (sy-originY)/scale, measure)
}

// editRenderedKey applies one keystroke to the focused rendered-mode text tile
// at its caret. All decisions — what a key does to (source, caret), the Enter
// paragraph contract, marker-skipping movement — live in the pure, unit-tested
// markdown.EditKey; this shim only gathers the inputs (focused editable tile,
// cached body, current layout), applies the result through the content store,
// and schedules the debounced save. A no-op unless the focused pane is editing
// an editable text tile in rendered mode; modifier combos stay with the
// browser (copy/paste/shortcuts).
func (a *App) editRenderedKey(ev js.Value) {
	p := a.tree.FocusedPane()
	if p == nil || p.TextMode != rpc.TextModeRendered || p.TextFocus == "" {
		return
	}
	if ev.Get("ctrlKey").Bool() || ev.Get("metaKey").Bool() || ev.Get("altKey").Bool() {
		return // copy/paste/shortcuts stay with the browser
	}
	gid := a.gridIDForPane(p)
	g, ok := a.c.Grid(gid)
	if !ok {
		return
	}
	file, ok := g.Tiles[p.TextFocus]
	if !ok || file.Kind != rpc.KindText || a.tileReadOnly(&file) {
		return
	}
	body, ok := a.tileBody(&file)
	if !ok {
		return
	}
	src := string(body)
	// No caret yet: an empty rendered doc has no text to click, so requiring a
	// prior click would make it impossible to type into. Default the caret to
	// the end of the source so the first keystroke just works.
	pl := a.local(p.ID)
	caret, hasCaret := pl.Caret()
	if !hasCaret {
		caret = len(src)
	}
	st := defaultMarkdownStyle()
	lstyle := markdownLayoutStyle(st)
	measure := a.markdownMeasure(st, textFixedScale)
	res := a.layoutMarkdown(src, a.textContentWidth(p), measure, lstyle)
	out := markdown.EditKey(src, caret, ev.Get("key").String(), res.Ops, lstyle, measure)
	if !out.Handled {
		return // function / media / dead keys — not ours
	}
	ev.Call("preventDefault")
	if out.Changed {
		// Write through the content store — the same accessor the renderer reads
		// (tileBody -> TileContent) — so the canvas reflects the keystroke now.
		a.c.PutTileContent(file.ID, []byte(out.Src))
		pl.Dirty = true
		a.scheduleFileSave()
		a.scheduleURLUpdate()
	}
	pl.SetCaret(out.Caret)
	a.draw()
}

// saveTextFromCache posts the focused rendered-mode tile's current cached body
// (already updated optimistically by each keystroke) and clears its dirty mark.
// The raw-text path is saveTextFromTextarea; this is its rendered-mode twin.
func (a *App) saveTextFromCache(p *pane.Pane) {
	gid := a.gridIDForPane(p)
	g, ok := a.c.Grid(gid)
	if !ok {
		// A dirty rendered-mode edit with nowhere to post — surface it, or
		// the typing silently never persists (charter §6).
		a.reportErr(errsurface.Error, "textedit", "text save failed — grid no longer loaded")
		return
	}
	file, ok := g.Tiles[p.TextFocus]
	if !ok {
		a.reportErr(errsurface.Error, "textedit", "text save failed — tile no longer exists")
		return
	}
	if file.Kind != rpc.KindText || a.tileReadOnly(&file) {
		return
	}
	body, ok := a.tileBody(&file)
	if !ok {
		return
	}
	if pl, ok := a.localIf(p.ID); ok {
		pl.Dirty = false
	}
	content := append([]byte(nil), body...)
	go func() {
		a.postUpdateText(gid, &rpc.UpdateTextRequest{
			Path:    rpc.Path{WellIDs: p.Path},
			TileID:  file.ID,
			Version: file.Version,
			Data:    content,
		}, content)
	}()
}

// placeMarkdownCaret sets pane p's rendered-mode caret to the source offset
// nearest the click (sx, sy), when the descended tile is an editable text tile.
// No-op for url/shell descents, read-only tiles, or clicks that hit-test to no
// text. The caret then renders on the next frame and anchors typing / drops.
func (a *App) placeMarkdownCaret(p *pane.Pane, r pane.Rect, sx, sy float64) {
	g, ok := a.c.Grid(a.gridIDForPane(p))
	if !ok {
		return
	}
	file, ok := g.Tiles[p.TextFocus]
	if !ok || file.Kind != rpc.KindText || a.tileReadOnly(&file) {
		return
	}
	off, ok := a.markdownCaretAt(p, r, &file, sx, sy)
	if !ok {
		return
	}
	a.local(p.ID).SetCaret(off)
}

// drawMarkdownCaret paints the rendered-mode editing caret for pane p (a
// vertical bar at the stored source offset), if one is set. Called from within
// the pane's clip after the markdown is painted. No-op when the pane has no
// caret. originX/originY/scale must match what drawMarkdownInRect used.
func (a *App) drawMarkdownCaret(p *pane.Pane, src string, originX, originY, scale float64) {
	pl, ok := a.localIf(p.ID)
	if !ok {
		return
	}
	off, ok := pl.Caret()
	if !ok {
		return
	}
	st := defaultMarkdownStyle()
	lstyle := markdownLayoutStyle(st)
	measure := a.markdownMeasure(st, scale)
	res := a.layoutMarkdown(src, a.textContentWidth(p), measure, lstyle)
	cx, cy, fontPx, ok := markdown.PointFromCaret(res.Ops, src, off, lstyle, measure)
	if !ok {
		return
	}
	c := a.cctx
	c.Set("fillStyle", st.textColor)
	// A 2px bar centered on the glyph box (CaretBar grows the em box a touch so
	// it doesn't hang below the text).
	top, ht := markdown.CaretBar(cy, fontPx)
	c.Call("fillRect", originX+cx*scale, originY+top*scale, 2.0, ht*scale)
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
