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
		if blob, ok := a.c.Blob(n.BlobID); ok {
			a.drawMarkdownInRect(string(blob),
				x-scrollX*scale, y-scrollY*scale,
				fileNaturalContentPx*scale, h+scrollY*scale,
				scale, mode, a.makeEmbedDrawer(p.ID))
		} else if n.BlobID != 0 {
			a.fetchBlob(n.BlobID)
		}
	} else if _, ok := a.c.Blob(n.BlobID); !ok && n.BlobID != 0 {
		a.fetchBlob(n.BlobID)
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
	var scale, scrollX, scrollY float64
	if fp != nil {
		if fp.TextMode != "" {
			mode = fp.TextMode
		}
		fpRect := a.paneRectByID(fp.ID)
		_, _, iw, ih := fileInnerBox(fp, fpRect)
		scrollX = fp.TextScrollX
		scrollY = fp.TextScrollY
		if iw > 0 && ih > 0 {
			scale = w / iw
			if sy := h / ih; sy > scale {
				scale = sy
			}
		} else {
			scale = fileFixedScale
		}
	} else if n.TextW > 0 && n.TextH > 0 {
		sxr := w / float64(n.TextW)
		syr := h / float64(n.TextH)
		scale = sxr
		if syr > scale {
			scale = syr
		}
		scrollX = float64(n.TextX)
		scrollY = float64(n.TextY)
	} else {
		scale = w / fileNaturalContentPx
		if scale < 0.02 {
			scale = 0.02
		}
	}

	a.cctx.Call("save")
	a.cctx.Call("beginPath")
	a.cctx.Call("rect", x, y, w, h)
	a.cctx.Call("clip")

	a.cctx.Set("fillStyle", colorFileInnerBg)
	a.cctx.Call("fillRect", x, y, w, h)

	hideForTextarea := fp != nil && fp.TextMode == rpc.TextModeText && fp.ID == a.tree.Focus
	if !hideForTextarea {
		if blob, ok := a.c.Blob(n.BlobID); ok {
			a.drawMarkdownInRect(string(blob),
				x-scrollX*scale, y-scrollY*scale,
				fileNaturalContentPx*scale, h+scrollY*scale,
				scale, mode, a.makePreviewEmbedDrawer())
		} else if n.BlobID != 0 {
			a.fetchBlob(n.BlobID)
		}
	} else if _, ok := a.c.Blob(n.BlobID); !ok && n.BlobID != 0 {
		a.fetchBlob(n.BlobID)
	}

	a.cctx.Call("restore")

	outlineColor := colorMarkdownLine
	if outside {
		outlineColor = colorExitBorder
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
	}
	// ColorText / ColorHeading / ColorCode and any syntax roles not yet wired.
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
	r := markdown.Layout(markdown.Lower([]byte(src)), m, embedpkg.SpanIsEmbed, width, style)
	a.mdCache[key] = r
	return r
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
func drawMarkdownText(c js.Value, src string, x, y, _ /* w */, h, scale, scrollY float64) {
	st := defaultMarkdownStyle()
	fontPx := st.codePx
	lineHeight := fontPx * 1.35
	setFont(c, fontPx*scale, st.monospace, false, false)
	c.Set("textBaseline", "top")
	c.Set("fillStyle", st.textColor)
	lines := strings.Split(src, "\n")
	cursorY := st.pad - scrollY
	for _, ln := range lines {
		yPx := cursorY * scale
		if yPx+lineHeight*scale > 0 && yPx < h {
			c.Call("fillText", ln, x+st.pad*scale, y+yPx)
		}
		cursorY += lineHeight
	}
}
