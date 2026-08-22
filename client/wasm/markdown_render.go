//go:build js && wasm

package main

import (
	"fmt"
	"strconv"
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/markdown"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/textedit"
)

// This file paints text tiles on the CANVAS: always the raw monospace
// source, soft-wrapped like the editing textarea (issue #216). The styled
// rendered view is a sanitized-HTML overlay div since issue #218
// (rendered_overlay.go, markdown.RenderHTML) — the custom canvas layout
// engine is deleted.

// textContentWidth is the logical width rendered markdown wraps at for pane p:
// the pane's inner reading-box width. Using the pane's own width (not a fixed
// 800px constant) is what reflows the doc to the pane — a split pane is narrower
// than 800px, so the old fixed width laid the doc out at 800 and the pane clip
// chopped the right edge ("cut off"). The painter and the textarea sizing
// read this one width so they stay in agreement; the preview
// lays out at the framing width it cover-crops (PreviewFrame.ContentW), which
// for a focused tile is this same value — so an unfocused pane is a true scaled
// copy of the focused one, not a re-wrap. (Scale is fixed at 1.0 in a descended
// file pane, so screen px and logical px coincide here.)
func (a *App) textContentWidth(p *pane.Pane) float64 {
	_, _, w, _ := textInnerBox(paneRectFor(a, p))
	// LOGICAL width: the wrap width the layout runs at, which the render
	// transform (textScaleFor) blows back up — so zooming re-wraps lines to
	// keep filling the pane, browser-style (issue #82).
	return w / a.textScaleFor(p)
}

// drawMarkdownInPane renders a text document as the contents of the pane
// currently descended into it, using that pane's live TextScroll so split
// panes scroll independently. The FOCUSED pane is covered by its overlay —
// the editing textarea in text mode, the rendered-HTML div in rendered mode
// (issue #218) — once the overlay has content; every other descended pane
// paints the raw source on canvas (HTML cannot be painted here, and an
// unfocused pane must still show the doc), soft-wrapped to the pane width
// exactly like the textarea (issue #216).
func (a *App) drawMarkdownInPane(p *pane.Pane, n *rpc.Tile, x, y, w, h float64) {
	scale := a.textScaleFor(p)
	originX := x - p.TextScrollX*scale
	originY := y - p.TextScrollY*scale

	a.cctx.Call("save")
	a.cctx.Call("beginPath")
	a.cctx.Call("rect", x, y, w, h)
	a.cctx.Call("clip")

	mode := p.TextMode
	if mode == "" {
		mode = rpc.TextModeRendered
	}
	ready := a.textareaReady
	if mode == rpc.TextModeRendered {
		ready = a.renderedReady
	}
	// textedit.CanvasHiddenByOverlay is the single owner of "canvas paints
	// vs overlay covers" — mode-agnostic since #218 (both modes are DOM
	// overlays on the focused pane); the ready guard keeps the canvas
	// painting through the loading race (issue #35).
	if !textedit.CanvasHiddenByOverlay(true, p.ID == a.tree.Focus, ready) {
		// A RENDERED-mode pane the overlay isn't covering (an unfocused
		// sibling; the focused pane during the overlay's load) paints the
		// rendered RASTER (#233's cache) — the pane must not flip to raw
		// source just because focus moved (#261: things stay as you leave
		// them; the overlay-vs-raster swap is an implementation detail the
		// user never sees). Raw is only the raster's own loading frame.
		if mode == rpc.TextModeRendered {
			frame := markdown.PreviewFrame{
				Scale:    scale,
				ScrollY:  p.TextScrollY,
				ContentW: a.textContentWidth(p),
			}
			if a.drawRenderedPreview(n, frame, x, y, w, h, 0) {
				// e2e attribution (renderedPreviews testhook): the pane
				// painted the RENDERED raster, not raw — the #261 pin.
				a.renderedPanePaints[n.ID]++
				a.cctx.Call("restore")
				return
			}
		}
		if body, ok := a.tileBody(n); ok {
			drawMarkdownText(a.cctx, string(body), originX, originY,
				a.textContentWidth(p), h+p.TextScrollY*scale, scale, 0, a.memoWrap(n))
		}
	} else {
		a.tileBody(n) // warm the cache so the overlay has content when shown
	}

	a.cctx.Call("restore")
}

// drawMarkdownNode renders a text tile at (x, y, w, h) as a grid preview.
// Constant-scale window (issue #205): the type size never follows grid
// zoom, the doc wraps to the tile's width (issue #216's raw wrap), and the
// stored scroll (TextX/TextY) places the window. The preview follows the
// tile's stored text_mode (issue #233): "rendered" draws the rasterized
// RenderHTML output (rendered_preview.go — no second layout engine, #218
// stands), anything else the raw source; the raw source also covers the
// async raster gap.
func (a *App) drawMarkdownNode(n *rpc.Tile, x, y, w, h float64, selected, outside, dashed bool) {
	frame := markdown.PreviewWindowFrame(w, textFixedScale, contentZoomOf(n), n.TextX, n.TextY)
	scale, scrollX, scrollY := frame.Scale, frame.ScrollX, frame.ScrollY

	a.cctx.Call("save")
	a.cctx.Call("beginPath")
	a.cctx.Call("rect", x, y, w, h)
	a.cctx.Call("clip")

	a.cctx.Set("fillStyle", colorFileInnerBg)
	a.cctx.Call("fillRect", x, y, w, h)

	// Content starts below the banner strip (drawTileBannerLabel paints over
	// the same box afterwards; bannerGeom is the shared formula) so the alt
	// text never overprints the first line.
	topInset := 0.0
	if tileBannerLabel(n) != "" {
		if _, bannerH, shown := bannerGeom(h, h-2*tileBorderPx); shown {
			topInset = bannerH
		}
	}
	if markdown.PreviewContentVisible(h-topInset, scale) {
		drawn := false
		if n.TextMode == rpc.TextModeRendered {
			drawn = a.drawRenderedPreview(n, frame, x, y, w, h, topInset)
		}
		if !drawn {
			if body, ok := a.tileBody(n); ok {
				drawMarkdownText(a.cctx, string(body),
					x-scrollX*scale, y+topInset-scrollY*scale,
					frame.ContentW, h-topInset+scrollY*scale, scale, 0, a.memoWrap(n))
			}
		}
	}

	a.cctx.Call("restore")

	// Host (outside) file tiles color by RENDERABILITY (issue #236, owner
	// decision revising the uniform brown): a file the markdown renderer
	// can show is text-green like any document; one it can't (metadata
	// only on descent) is muted grey. markdown.Renderable is the same rule
	// the fs plugin serves bodies by, so the color never lies.
	outlineColor := colorMarkdownLine
	if outside && !markdown.Renderable(n.AltText) {
		outlineColor = colorMuted
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

// markdownStyle holds the raw-text painter's font/spacing/color parameters
// in logical pixels (before scale). The rest of the old canvas layout
// engine's parameters died with it (#218); these four are what the raw
// soft-wrap painter and the textarea sizing still read.
type markdownStyle struct {
	codePx    float64
	pad       float64
	monospace string
	textColor string
}

func defaultMarkdownStyle() markdownStyle {
	return markdownStyle{
		codePx:    13,
		pad:       6,
		monospace: `ui-monospace, "SF Mono", Menlo, Consolas, monospace`,
		textColor: "#d8d9de",
	}
}

// setFont assembles a CSS font shorthand and assigns it. size is in pixels.
func setFont(c js.Value, sizePx float64, family string, bold bool) {
	weight := "normal"
	if bold {
		weight = "bold"
	}
	if sizePx < 1 {
		sizePx = 1
	}
	c.Set("font", fmt.Sprintf("normal %s %.2fpx %s", weight, sizePx, family))
}

// rawTextLineHeight is the line-advance multiple for raw monospace markdown
// source. Shared by the canvas painter (drawMarkdownText) and the editing
// <textarea> (text_overlay.go) so a focused pane's textarea and its blurred
// canvas preview render line-for-line identically — the same content, the
// same size, the same place, whether or not the pane has focus.
const rawTextLineHeight = 1.35

// drawMarkdownText paints src as raw monospace text at the given scale. Used
// for source-mode preview and as a faint backdrop behind the textarea overlay.
// Text mode soft-wraps to the SAME columns the editing textarea shows
// (markdown.WrapRawText, issue #216) — the face is monospace, so the budget
// is a pure column count and the text cannot reflow when focus moves.
func drawMarkdownText(c js.Value, src string, x, y, w, h, scale, scrollY float64,
	wrap func(src string, cols int) []string) {
	st := defaultMarkdownStyle()
	fontPx := st.codePx
	setFont(c, fontPx*scale, st.monospace, false)
	c.Set("fillStyle", st.textColor)
	// Place each line's baseline exactly where a CSS line box would, so this
	// matches the editing <textarea> (text_overlay.go) to the pixel and the
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
	for _, ln := range wrap(src, rawWrapCols(m, w, scale, st.pad)) {
		if slotTop >= h {
			break // past the bottom edge — nothing below is visible
		}
		if markdown.RawTextLineVisible(slotTop, slotted.Slot, h) {
			c.Call("fillText", ln, x+st.pad*scale, y+slotTop+slotted.Baseline)
		}
		slotTop += slotted.Slot
	}
}

// memoWrap is drawMarkdownText's wrap provider backed by a render cache:
// re-wrapping a whole document every frame for every visible file tile
// was O(doc × tiles) per frame (#265). Keyed by content id + version +
// length + columns — the version bump (the one content door) invalidates
// committed edits; the length guards the brief in-flight-edit window
// (same-length uncommitted edits may render one debounce-cycle stale in
// a background preview, which the save's version bump then corrects).
// Bounded by wholesale reset: it is a derived cache, never a fact.
func (a *App) memoWrap(n *rpc.Tile) func(string, int) []string {
	return func(src string, cols int) []string {
		key := n.ContentID() + "\x00" + strconv.FormatInt(n.Version, 10) + "\x00" +
			strconv.Itoa(len(src)) + "\x00" + strconv.Itoa(cols)
		if lines, ok := a.wrapCache[key]; ok {
			return lines
		}
		lines := markdown.WrapRawText(src, cols)
		if len(a.wrapCache) >= 512 {
			a.wrapCache = map[string][]string{}
		}
		a.wrapCache[key] = lines
		return lines
	}
}

// rawWrapCols is the soft-wrap column budget for raw text painted into a
// box w LOGICAL units wide at scale: the pixel content width (minus the
// pad the painter insets by) over one monospace advance. m is the already-
// scaled measureText result the painter took for its slot math.
func rawWrapCols(m js.Value, w, scale, pad float64) int {
	adv := m.Get("width").Float()
	if adv <= 0 {
		return 0
	}
	return int(((w - 2*pad) * scale) / adv)
}
