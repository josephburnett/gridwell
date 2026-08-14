//go:build js && wasm

package main

import (
	"math"
	"strconv"
	"syscall/js"

	"github.com/josephburnett/gridwell/client/markdown"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// Rendered grid previews (issue #233): a text tile whose stored text_mode
// is "rendered" previews as the RENDERED document — how you leave a tile
// is how it presents from outside. The #218 decision stands untouched:
// markdown.RenderHTML is the ONE renderer; this file rasterizes its
// sanitized output through an SVG foreignObject image and draws that
// bitmap into the preview. There is no second layout engine.
// Rasterization is async, so the raw source paints until the image
// decodes (the same "canvas paints until ready" shape as the overlays).

// renderedPreviewMaxH caps the rasterized document height in CSS px. A
// preview window scrolled beyond the cap falls back to raw source —
// previews are a glance, not a reader.
const renderedPreviewMaxH = 4000.0

// renderedPreviewBucket quantizes the layout width so continuous grid zoom
// re-rasterizes at steps, not per frame; drawImage stretches the current
// raster between steps.
const renderedPreviewBucket = 64.0

// renderedPreview is one tile's cached raster.
type renderedPreview struct {
	key     string
	img     js.Value
	url     string
	rasterW float64
	ready   bool
	failed  bool
}

// renderedPreviewFor returns the raster for tile n laid out at (roughly)
// logical width contentW, kicking an async rasterization on a cache miss.
// ok is false until the image has decoded — and stays false on a failed
// decode — so the caller paints raw source in the meantime.
func (a *App) renderedPreviewFor(n *rpc.Tile, contentW float64) (*renderedPreview, bool) {
	bucket := math.Max(renderedPreviewBucket,
		math.Round(contentW/renderedPreviewBucket)*renderedPreviewBucket)
	isOrg := markdown.IsOrg(n.AltText)
	key := n.ID + "\x00" + strconv.FormatInt(n.Version, 10) + "\x00" +
		strconv.FormatFloat(bucket, 'f', 0, 64) + "\x00" + strconv.FormatBool(isOrg)
	if e, ok := a.renderedPrev[n.ID]; ok && e.key == key {
		return e, e.ready && !e.failed
	}
	body, ok := a.tileBody(n)
	if !ok {
		return nil, false // blob fetch in flight; the raw path warms it too
	}
	// (dropRenderedPreview is the deletion twin of this replace-time revoke.)
	if old, ok := a.renderedPrev[n.ID]; ok && old.url != "" {
		js.Global().Get("URL").Call("revokeObjectURL", old.url)
	}
	e := &renderedPreview{key: key, rasterW: bucket}
	a.renderedPrev[n.ID] = e

	// Serialize the sanitized render through the DOM so goldmark's HTML5
	// output (unclosed <br>, <img>) becomes well-formed XML — the SVG
	// foreignObject is an XML context.
	div := a.doc.Call("createElement", "div")
	div.Set("innerHTML", presentationHTML(n, body))
	xhtml := js.Global().Get("XMLSerializer").New().Call("serializeToString", div).String()
	svg := markdown.PreviewSVG(xhtml, bucket, renderedPreviewMaxH, colorFileInnerBg)

	blob := js.Global().Get("Blob").New(
		js.ValueOf([]any{svg}), js.ValueOf(map[string]any{"type": "image/svg+xml"}))
	e.url = js.Global().Get("URL").Call("createObjectURL", blob).String()
	img := js.Global().Get("Image").New()
	var onload, onerror js.Func
	release := func() { onload.Release(); onerror.Release() }
	onload = js.FuncOf(func(js.Value, []js.Value) any {
		e.ready = true
		release()
		a.draw()
		return nil
	})
	onerror = js.FuncOf(func(js.Value, []js.Value) any {
		e.failed = true // raw source stays the preview; never retry-loop
		release()
		return nil
	})
	img.Set("onload", onload)
	img.Set("onerror", onerror)
	img.Set("src", e.url)
	e.img = img
	return e, false
}

// drawRenderedPreview windows the tile's raster into (x, y+topInset, w,
// h-topInset) at the preview frame's scroll, reporting whether it drew.
// false (raster pending/failed/scrolled past the cap) → raw fallback.
func (a *App) drawRenderedPreview(n *rpc.Tile, frame markdown.PreviewFrame,
	x, y, w, h, topInset float64) bool {
	e, ok := a.renderedPreviewFor(n, frame.ContentW)
	if !ok {
		return false
	}
	s := w / e.rasterW
	if s <= 0 {
		return false
	}
	sy := frame.ScrollY
	sh := (h - topInset) / s
	if sy < 0 || sy >= renderedPreviewMaxH {
		return false
	}
	if sy+sh > renderedPreviewMaxH {
		sh = renderedPreviewMaxH - sy
	}
	a.cctx.Call("drawImage", e.img, 0, sy, e.rasterW, sh,
		x, y+topInset, w, sh*s)
	return true
}

// dropRenderedPreview releases a removed tile's rendered-preview entry:
// the blob object URL is revoked and the decoded raster freed with the
// map entry. Fired from the TileRemoved event arm, beside urlPreview.Drop
// — the two preview caches must age out together or deleting text tiles
// leaks image resources for the life of the page.
func (a *App) dropRenderedPreview(tileID string) {
	e, ok := a.renderedPrev[tileID]
	if !ok {
		return
	}
	if e.url != "" {
		js.Global().Get("URL").Call("revokeObjectURL", e.url)
	}
	delete(a.renderedPrev, tileID)
}
