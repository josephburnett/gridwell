//go:build js && wasm

package main

import (
	"fmt"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"strconv"
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/contentzoom"
	"github.com/josephburnett/gridwell/client/markdown"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/textedit"
	"github.com/josephburnett/gridwell/client/tilebanner"
)

// Paints text tiles on the canvas: the raw monospace source, soft-wrapped
// like the editing textarea. The styled rendered view is a sanitized-HTML
// overlay div, in rendered_overlay.go.

// textContentWidth is the logical width rendered markdown wraps at for pane
// p. The pane's own width is what reflows the doc to it; a fixed width would
// lay it out wider than a split pane. The painter, the textarea sizing and
// the preview's ContentW all read this one width, so an unfocused pane is a
// scaled copy rather than a re-wrap.
func (a *App) textContentWidth(p *pane.Pane) float64 {
	_, _, w, _ := textInnerBox(paneRectFor(a, p))
	// The wrap width the layout runs at, which textScaleFor blows back up, so
	// zooming re-wraps lines to keep filling the pane.
	return w / a.textScaleFor(p)
}

// drawMarkdownInPane renders a text document in the pane descended into it,
// on that pane's own TextScroll so split panes scroll independently. The
// focused pane is covered by its overlay once the overlay has content; every
// other descended pane paints on canvas, because HTML cannot be painted
// here.
func (a *App) drawMarkdownInPane(p *pane.Pane, n *gridwellv1.Tile, x, y, w, h float64) {
	scale := a.textScaleFor(p)
	originX := x - p.TextScrollX*scale
	originY := y - p.TextScrollY*scale

	withClip(a.cctx, x, y, w, h, func() {
		mode := p.TextMode
		if mode == "" {
			mode = rpc.TextModeRendered
		}
		ready := a.overlays.textareaReady
		if mode == rpc.TextModeRendered {
			ready = a.overlays.renderedReady
		}
		// textedit.CanvasHiddenByOverlay is the one owner of "canvas paints
		// or overlay covers", and the ready guard keeps the canvas painting
		// through the loading race.
		if !textedit.CanvasHiddenByOverlay(true, p.ID == a.tree.Focus, ready) {
			// A rendered-mode pane the overlay is not covering paints the
			// rendered raster: the pane must not flip to raw source just
			// because focus moved. Raw is only the raster's loading frame.
			if mode == rpc.TextModeRendered {
				frame := markdown.PreviewFrame{
					Scale:    scale,
					ScrollY:  p.TextScrollY,
					ContentW: a.textContentWidth(p),
				}
				if a.drawRenderedPreview(n, frame, x, y, w, h, 0) {
					// e2e attribution, read by the renderedPreviews testhook.
					a.renderedPanePaints[n.Id]++
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
	})
}

// drawMarkdownNode renders a text tile as a grid preview: a constant-scale
// window, so the type size never follows grid zoom, the doc wraps to the
// tile's width, and the stored TextX/TextY place the window. The preview
// follows the tile's stored text_mode, and raw source covers the async raster
// gap.
func (a *App) drawMarkdownNode(n *gridwellv1.Tile, x, y, w, h float64, selected, outside, dashed bool) {
	frame := markdown.PreviewWindowFrame(w, textFixedScale, contentzoom.Of(n.GetContentZoom()), n.TextX, n.TextY)
	scale, scrollX, scrollY := frame.Scale, frame.ScrollX, frame.ScrollY

	withClip(a.cctx, x, y, w, h, func() {
		a.cctx.Set("fillStyle", colorFileInnerBg)
		a.cctx.Call("fillRect", x, y, w, h)

		// Content starts below the banner strip, on bannerGeom's shared
		// formula, so the alt text never overprints the first line.
		topInset := 0.0
		if label, _ := tilebanner.Runs(n); label != "" {
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
	})

	// A host file the markdown renderer can show is text-green like any
	// document, and one it cannot is muted grey. markdown.Renderable is the
	// same rule the fs plugin serves bodies by, so the color never lies.
	outlineColor := colorMarkdownLine
	if outside && !markdown.Renderable(n.AltText) {
		outlineColor = colorMuted
	}
	strokeTileFrame(a.cctx, x, y, w, h, outlineColor, dashed, selected)
}

// markdownStyle is the raw-text painter's font, spacing and color in logical
// pixels, before scale. The soft-wrap painter and the textarea sizing read
// it.
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

// rawTextLineHeight is the line-advance multiple for raw monospace source,
// shared by the canvas painter and the editing textarea so the two render
// line-for-line identically whether or not the pane has focus.
const rawTextLineHeight = 1.35

// drawMarkdownText paints src as raw monospace text at the given scale. It
// soft-wraps to the same columns the editing textarea shows: the face is
// monospace, so the budget is a pure column count and the text cannot reflow
// when focus moves.
func drawMarkdownText(c js.Value, src string, x, y, w, h, scale, scrollY float64,
	wrap func(src string, cols int) []string) {
	st := defaultMarkdownStyle()
	fontPx := st.codePx
	setFont(c, fontPx*scale, st.monospace, false)
	c.Set("fillStyle", st.textColor)
	// Each line's baseline goes exactly where a CSS line box would put it, so
	// this matches the editing textarea to the pixel and the raw text does
	// not shift when focus enters or leaves the pane.
	c.Set("textBaseline", "alphabetic")
	m := c.Call("measureText", "M")
	asc := m.Get("fontBoundingBoxAscent").Float()
	desc := m.Get("fontBoundingBoxDescent").Float()
	// markdown.RawTextLineSlot is the pixel-match-the-textarea contract. asc
	// and desc come from the scaled canvas font above.
	slotted := markdown.RawTextLineSlot(fontPx, rawTextLineHeight, scale, st.pad, scrollY, asc, desc)
	slotTop := slotted.Top0
	for _, ln := range wrap(src, rawWrapCols(m, w, scale, st.pad)) {
		if slotTop >= h {
			break // nothing below the bottom edge is visible
		}
		if markdown.RawTextLineVisible(slotTop, slotted.Slot, h) {
			c.Call("fillText", ln, x+st.pad*scale, y+slotTop+slotted.Baseline)
		}
		slotTop += slotted.Slot
	}
}

// memoWrap caches the wrap, because re-wrapping every visible document each
// frame costs O(doc x tiles). Keyed by content id, version, length and
// columns, so a same-length uncommitted edit may render one debounce cycle
// stale in a background preview. Bounded by wholesale reset, since it is
// derived and never a fact.
func (a *App) memoWrap(n *gridwellv1.Tile) func(string, int) []string {
	return func(src string, cols int) []string {
		key := rpc.ContentID(n) + "\x00" + strconv.FormatInt(n.Version, 10) + "\x00" +
			strconv.Itoa(len(src)) + "\x00" + strconv.Itoa(cols)
		if lines, ok := a.views.wrapCache[key]; ok {
			return lines
		}
		lines := markdown.WrapRawText(src, cols)
		if len(a.views.wrapCache) >= 512 {
			a.views.wrapCache = map[string][]string{}
		}
		a.views.wrapCache[key] = lines
		return lines
	}
}

// rawWrapCols is the soft-wrap column budget for raw text in a box w logical
// units wide: the pixel content width less the painter's pad, over one
// monospace advance. m is the painter's already-scaled measureText result.
func rawWrapCols(m js.Value, w, scale, pad float64) int {
	adv := m.Get("width").Float()
	if adv <= 0 {
		return 0
	}
	return int(((w - 2*pad) * scale) / adv)
}
