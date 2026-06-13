//go:build js && wasm

package main

import (
	"fmt"
	"strings"
	"syscall/js"

	embedpkg "github.com/josephburnett/gridwell/client/embed"
	"github.com/josephburnett/gridwell/client/markdown"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// This file holds all markdown rendering for the canvas: the App-method
// entry points (preview vs live-pane) and the pure layout/paint engine that
// turns client/markdown's parsed blocks into canvas draw calls.

// drawMarkdownInPane renders a markdown file as the contents of the
// pane that is currently descended into it. Uses *that* pane's live
// TextMode / TextZoom / TextScroll values, so two split panes both
// looking at the same file can each scroll/zoom independently.
//
// Also responsible for skipping the canvas render in text mode for
// the focused pane (the textarea overlay handles that one). Other
// panes still render the source as canvas text so the user has
// continuity in non-focused split siblings.
func (a *App) drawMarkdownInPane(p *pane.Pane, n *rpc.Tile, x, y, w, h float64) {
	mode := p.TextMode
	if mode == "" {
		mode = rpc.TextModeRendered
	}
	// Fixed scale: the pane is a plain window onto the document. No zoom.
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
			drawMarkdownInRect(a.cctx, string(blob),
				x-scrollX*scale, y-scrollY*scale,
				fileNaturalContentPx*scale, h+scrollY*scale,
				scale, 0, mode, a.makeEmbedDrawer(p.ID))
		} else if n.BlobID != 0 {
			a.fetchBlob(n.BlobID)
		}
	} else if _, ok := a.c.Blob(n.BlobID); !ok && n.BlobID != 0 {
		a.fetchBlob(n.BlobID)
	}

	a.cctx.Call("restore")
}

// drawMarkdownNode renders a markdown file tile at (x, y, w, h).
//
// Two distinct rendering modes:
//
//  1. Preview (no pane is descended into this file). Scale is clamped
//     to <= 1.0 so the rendered text never grows past natural reading
//     size, regardless of how far the user has zoomed into the parent
//     grid. A small file appears as a small but readable preview; a
//     large parent zoom doesn't blow it up.
//  2. Live text mode (a pane is descended into this file). The scale
//     is the pane's TextZoom (independent of parent zoom) and the
//     visible region is taken from TextScrollX/TextScrollY. Wheel
//     events update those fields directly so navigation is buttery.
//
// In both modes the parent grid lines remain visible behind the text
// (no fill), and an outline marks the footprint.
func (a *App) drawMarkdownNode(n *rpc.Tile, x, y, w, h float64, _ pane.Rect, selected, outside, dashed bool) {
	// Mode comes from the tile (persisted on the server); default to raw
	// text for a never-opened file. A pane descended into this file
	// overrides with its live mode.
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
		// While a pane is descended, mirror what the preview will be once
		// the user ascends: cover-crop the LIVE framed window (live scroll
		// + the focused pane's current inner-box size) into this tile.
		// Rendering at raw 1:1 instead made the text look too small in a
		// small tile and rescale as the parent grid zoomed.
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
		// Preview mirrors the last framed window: cover-crop the saved
		// document-space rectangle (TextX,TextY,TextW,TextH) into the
		// tile footprint (x,y,w,h). The larger axis ratio binds (cover),
		// anchored at the window's top-left so headings stay in view.
		// w/h already include the parent grid zoom, so the label scales
		// naturally as the user zooms the parent grid.
		sxr := w / float64(n.TextW)
		syr := h / float64(n.TextH)
		scale = sxr
		if syr > scale {
			scale = syr
		}
		scrollX = float64(n.TextX)
		scrollY = float64(n.TextY)
	} else {
		// Never framed: fit the document width to the tile, top-aligned.
		scale = w / fileNaturalContentPx
		if scale < 0.02 {
			scale = 0.02
		}
	}

	a.cctx.Call("save")
	a.cctx.Call("beginPath")
	a.cctx.Call("rect", x, y, w, h)
	a.cctx.Call("clip")

	// Paint the full cell with the same light-grey background that
	// live mode uses for its inner-box. The user sees the cell as the
	// preview unit; the grey = cell.
	a.cctx.Set("fillStyle", colorFileInnerBg)
	a.cctx.Call("fillRect", x, y, w, h)

	// When the focused pane is descended into THIS file in text mode,
	// the textarea overlay renders the editable source. Drawing the
	// markdown to the canvas behind it would just produce a doubled,
	// misaligned render, so skip it.
	hideForTextarea := fp != nil && fp.TextMode == rpc.TextModeText && fp.ID == a.tree.Focus
	if !hideForTextarea {
		if blob, ok := a.c.Blob(n.BlobID); ok {
			// Layout width is fixed at the natural content width so
			// every preview wraps lines the same way live mode does —
			// principle of continuity across the path swap. Text that
			// would extend past the cell to the right (because live
			// inner-box is wider than tall) gets clipped at the cell
			// edge — that's the "start from the left" behavior the
			// user asked for.
			// Preview embeds render the same as live embeds (kind-tinted
			// thumbnail) but skip hit registration — clicking a preview
			// embed descends into the parent text tile, not the embed's
			// target. Live mode wires the hit list via makeEmbedDrawer.
			drawMarkdownInRect(a.cctx, string(blob),
				x-scrollX*scale, y-scrollY*scale,
				fileNaturalContentPx*scale, h+scrollY*scale,
				scale, 0, mode, a.makePreviewEmbedDrawer())
		} else if n.BlobID != 0 {
			a.fetchBlob(n.BlobID)
		}
	} else if _, ok := a.c.Blob(n.BlobID); !ok && n.BlobID != 0 {
		a.fetchBlob(n.BlobID)
	}

	a.cctx.Call("restore")

	// Outline color follows the color grammar: green for in-Gridwell text
	// tiles, red when this tile represents something outside (clone of a
	// source-grid file, or rendered inside a source grid).
	outlineColor := colorMarkdownLine
	if outside {
		outlineColor = colorExitBorder
	}
	// Dashed when this is a file dragged out as a link into a regular grid.
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

// drawMarkdownInRect lays out and paints `src` (markdown source) into the
// rectangle (x, y, w, h) at the given `scale` (1.0 = base sizes), starting
// at vertical scroll offset `scrollY` measured in logical pixels (i.e.,
// the units the layout would use at scale 1.0). The mode picks between
// rendered (block layout with headings/bold/etc.) and text (raw source as
// monospace).
//
// The rect's clip is the caller's responsibility.
func drawMarkdownInRect(c js.Value, src string, x, y, w, h, scale, scrollY float64, mode string, drawEmbed embedDrawer) {
	if mode == rpc.TextModeText {
		drawMarkdownText(c, src, x, y, w, h, scale, scrollY)
		return
	}
	drawMarkdownRendered(c, src, x, y, w, h, scale, scrollY, drawEmbed)
}

// markdownStyle holds the per-block-kind font/spacing parameters in
// logical pixels (i.e., before applying scale).
type markdownStyle struct {
	bodyPx     float64
	h1Px       float64
	h2Px       float64
	h3Px       float64
	codePx     float64
	pad        float64 // logical px gutter at top/left of layout
	gapAfter   float64 // logical px below each block
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

// blockFontSize returns the font size in logical pixels for a block kind.
func (s markdownStyle) blockFontSize(k markdown.BlockKind) float64 {
	switch k {
	case markdown.BlockHeading1:
		return s.h1Px
	case markdown.BlockHeading2:
		return s.h2Px
	case markdown.BlockHeading3:
		return s.h3Px
	case markdown.BlockCode:
		return s.codePx
	}
	return s.bodyPx
}

// blockFamily returns the font family for a block kind.
func (s markdownStyle) blockFamily(k markdown.BlockKind) string {
	if k == markdown.BlockCode {
		return s.monospace
	}
	return s.sansSerif
}

// drawMarkdownRendered does block-level layout of `src` and paints it
// scaled by `scale` into (x, y, w, h), scrolled vertically by scrollY
// logical pixels (so scrollY=0 shows from the top).
func drawMarkdownRendered(c js.Value, src string, x, y, w, h, scale, scrollY float64, drawEmbed embedDrawer) {
	st := defaultMarkdownStyle()
	blocks := markdown.Parse(src)
	contentWidthLogical := (w / scale) - 2*st.pad
	if contentWidthLogical < 8 {
		contentWidthLogical = 8
	}

	c.Set("textBaseline", "top")
	c.Set("fillStyle", st.textColor)

	cursorY := st.pad - scrollY
	for _, b := range blocks {
		fontPx := st.blockFontSize(b.Kind)
		family := st.blockFamily(b.Kind)
		lineHeight := fontPx * 1.35

		switch b.Kind {
		case markdown.BlockBlank:
			cursorY += st.bodyPx * 0.6
			continue
		case markdown.BlockCode:
			// Code block: monospace, optional background tint.
			text := ""
			if len(b.Spans) > 0 {
				text = b.Spans[0].Text
			}
			lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
			blockHeight := lineHeight * float64(len(lines))
			top := cursorY * scale
			if top+blockHeight*scale > 0 && top < h {
				c.Set("fillStyle", st.codeBg)
				c.Call("fillRect", x+st.pad*scale, y+top, contentWidthLogical*scale, blockHeight*scale)
			}
			setFont(c, fontPx*scale, family, false, false)
			c.Set("fillStyle", st.textColor)
			for _, ln := range lines {
				yPx := cursorY * scale
				if yPx+lineHeight*scale > 0 && yPx < h {
					c.Call("fillText", ln, x+st.pad*scale+4, y+yPx)
				}
				cursorY += lineHeight
			}
			cursorY += st.gapAfter
			continue
		case markdown.BlockBlockquote:
			// Vertical bar in the gutter; indent text.
			topLogical := cursorY
			indent := 12.0
			lines := wrapInline(c, b.Spans, contentWidthLogical-indent, fontPx, family, scale)
			blockHeight := lineHeight * float64(len(lines))
			topPx := topLogical * scale
			if topPx+blockHeight*scale > 0 && topPx < h {
				c.Set("fillStyle", st.quoteBar)
				c.Call("fillRect", x+st.pad*scale, y+topPx, 3*scale, blockHeight*scale)
			}
			c.Set("fillStyle", st.mutedColor)
			drawInlineLines(c, lines, x+(st.pad+indent)*scale, y, cursorY, lineHeight, fontPx, family, scale, h, drawEmbed)
			cursorY += blockHeight + st.gapAfter
			continue
		case markdown.BlockListItem:
			// Bullet, then wrapped inline text.
			indent := 14.0
			lines := wrapInline(c, b.Spans, contentWidthLogical-indent, fontPx, family, scale)
			// Bullet sits on the first line.
			yPx := cursorY * scale
			if yPx+lineHeight*scale > 0 && yPx < h {
				setFont(c, fontPx*scale, family, false, false)
				c.Set("fillStyle", st.textColor)
				c.Call("fillText", "•", x+(st.pad+4)*scale, y+yPx)
			}
			drawInlineLines(c, lines, x+(st.pad+indent)*scale, y, cursorY, lineHeight, fontPx, family, scale, h, drawEmbed)
			cursorY += lineHeight*float64(len(lines)) + st.gapAfter
			continue
		}
		// Headings and paragraphs.
		lines := wrapInline(c, b.Spans, contentWidthLogical, fontPx, family, scale)
		drawInlineLines(c, lines, x+st.pad*scale, y, cursorY, lineHeight, fontPx, family, scale, h, drawEmbed)
		cursorY += lineHeight*float64(len(lines)) + st.gapAfter
	}
}

// wrapInline measures the spans and wraps them into lines that fit
// contentWidthLogical at the given font size. The font is set per-call so
// measureText reflects the right metrics; scale is applied uniformly so
// the wrap matches what the caller will paint.
func wrapInline(c js.Value, spans []markdown.Span, contentWidthLogical, fontPx float64, family string, scale float64) [][]markdown.Span {
	measure := func(sp markdown.Span) float64 {
		if embedpkg.SpanIsEmbed(sp) {
			ew, _ := embedLogicalSize(sp)
			return ew
		}
		setFont(c, fontPx*scale, family, sp.Style&markdown.StyleBold != 0, sp.Style&markdown.StyleItalic != 0)
		mt := c.Call("measureText", sp.Text)
		// Convert measured pixels back to logical units.
		return mt.Get("width").Float() / scale
	}
	return markdown.Wrap(spans, contentWidthLogical, measure)
}

// embedLogicalSize is the wasm-side adapter that binds the renderer's
// defaults to the pure embed.SpanEmbedSize.
func embedLogicalSize(sp markdown.Span) (float64, float64) {
	return embedpkg.SpanEmbedSize(sp, defaultEmbedW, defaultEmbedH)
}

// drawInlineLines paints wrapped inline lines starting at logical
// (xPx, baseTopLogical) with the given lineHeight; clips drawing to (y, h)
// in screen pixels. Embeds are dispatched to drawEmbed (when non-nil) and
// fall back to their alt text when no drawer is supplied.
func drawInlineLines(c js.Value, lines [][]markdown.Span, xPx, yBase, baseTopLogical, lineHeight, fontPx float64, family string, scale, h float64, drawEmbed embedDrawer) {
	for li, line := range lines {
		yLogical := baseTopLogical + float64(li)*lineHeight
		yPx := yLogical * scale
		if yPx+lineHeight*scale < 0 || yPx > h {
			continue
		}
		curX := xPx
		for _, sp := range line {
			if sp.Style&markdown.StyleEmbed != 0 {
				ewLogical, ehLogical := embedLogicalSize(sp)
				ew := ewLogical * scale
				eh := ehLogical * scale
				if drawEmbed != nil {
					drawEmbed(curX, yBase+yPx, ew, eh, sp.Href, sp.Alt)
				} else {
					// Preview / no-embed context: render the alt text inline.
					setFont(c, fontPx*scale, family, false, true)
					c.Set("fillStyle", colorMuted)
					label := sp.Alt
					if label == "" {
						label = "[embed]"
					}
					c.Call("fillText", label, curX, yBase+yPx)
				}
				curX += ew
				continue
			}
			if sp.Style&markdown.StyleLink != 0 {
				// If the href looks like a tile descent path, paint a
				// preview embed rather than a plain text link — that's
				// how the plain `[alt](href)` form becomes an embed
				// inside Gridwell while staying a normal link outside.
				if drawEmbed != nil && embedpkg.LeafTileIDFromHref(sp.Href) != 0 {
					ewLogical, ehLogical := embedLogicalSize(sp)
					ew := ewLogical * scale
					eh := ehLogical * scale
					drawEmbed(curX, yBase+yPx, ew, eh, sp.Href, sp.Text)
					curX += ew
					continue
				}
				setFont(c, fontPx*scale, family,
					sp.Style&markdown.StyleBold != 0,
					sp.Style&markdown.StyleItalic != 0)
				c.Set("fillStyle", colorURLLine)
				c.Call("fillText", sp.Text, curX, yBase+yPx)
				w := c.Call("measureText", sp.Text).Get("width").Float()
				// Underline.
				c.Set("fillStyle", colorURLLine)
				c.Call("fillRect", curX, yBase+yPx+fontPx*scale*1.05, w, 1)
				curX += w
				continue
			}
			bold := sp.Style&markdown.StyleBold != 0
			italic := sp.Style&markdown.StyleItalic != 0
			code := sp.Style&markdown.StyleCode != 0
			family2 := family
			if code {
				family2 = `ui-monospace, "SF Mono", Menlo, Consolas, monospace`
			}
			setFont(c, fontPx*scale, family2, bold, italic)
			if code {
				c.Set("fillStyle", colorMarkdownCodeBg2)
				w := c.Call("measureText", sp.Text).Get("width").Float()
				c.Call("fillRect", curX-2, yBase+yPx, w+4, lineHeight*scale)
			}
			c.Set("fillStyle", colorMarkdownBody)
			c.Call("fillText", sp.Text, curX, yBase+yPx)
			w := c.Call("measureText", sp.Text).Get("width").Float()
			curX += w
		}
	}
}

// setFont assembles a CSS font shorthand string and assigns it. Bold/italic
// are optional; size is in pixels.
func setFont(c js.Value, sizePx float64, family string, bold, italic bool) {
	style := "normal"
	if italic {
		style = "italic"
	}
	weight := "normal"
	if bold {
		weight = "bold"
	}
	// Browsers refuse fonts at 0px; clamp to a tiny minimum so the call doesn't error.
	if sizePx < 1 {
		sizePx = 1
	}
	c.Set("font", fmt.Sprintf("%s %s %.2fpx %s", style, weight, sizePx, family))
}

// drawMarkdownText paints src as raw monospace text at the same scale the
// rendered view would use. Used for the source-mode preview and as a faint
// backdrop while the textarea overlay is painted on top. The w parameter is
// unused: text mode does not soft-wrap; long lines are clipped by the
// caller's clip rect.
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
