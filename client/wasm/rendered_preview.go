//go:build js && wasm

package main

import (
	"math"
	"strconv"
	"strings"
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/markdown"
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
//
// The cache is keyed PER (tile, width bucket), not per tile (#261's
// investigation): two consumers of the same tile at different widths —
// two split panes, or two grid previews at different zooms — used to
// replace a single per-tile entry every frame, each creation revoking
// the OTHER's still-loading blob URL, so no raster ever decoded (a
// latent #233 bug; the pane path made it constant). Stale-VERSION
// entries for the same tile are swept at insert; whole-tile cleanup is
// dropRenderedPreview (TileRemoved).
func (a *App) renderedPreviewFor(n *rpc.Tile, contentW float64) (*renderedPreview, bool) {
	bucket := math.Max(renderedPreviewBucket,
		math.Round(contentW/renderedPreviewBucket)*renderedPreviewBucket)
	isOrg := markdown.IsOrg(n.AltText)
	mapKey := n.ID + "\x00" + strconv.FormatFloat(bucket, 'f', 0, 64)
	key := n.ID + "\x00" + strconv.FormatInt(n.Version, 10) + "\x00" +
		strconv.FormatFloat(bucket, 'f', 0, 64) + "\x00" + strconv.FormatBool(isOrg)
	if e, ok := a.renderedPrev[mapKey]; ok && e.key == key {
		return e, e.ready && !e.failed
	}
	body, ok := a.tileBody(n)
	if !ok {
		return nil, false // blob fetch in flight; the raw path warms it too
	}
	// Replace a stale same-bucket entry, and sweep OTHER buckets of this
	// tile whose version moved on (they re-rasterize on next use).
	// (dropRenderedPreview is the deletion twin of these revokes.)
	if old, ok := a.renderedPrev[mapKey]; ok && old.url != "" {
		js.Global().Get("URL").Call("revokeObjectURL", old.url)
	}
	stalePrefix := n.ID + "\x00"
	for mk, old := range a.renderedPrev {
		if mk != mapKey && strings.HasPrefix(mk, stalePrefix) && old.key != "" &&
			!strings.HasPrefix(old.key, n.ID+"\x00"+strconv.FormatInt(n.Version, 10)+"\x00") {
			if old.url != "" {
				js.Global().Get("URL").Call("revokeObjectURL", old.url)
			}
			delete(a.renderedPrev, mk)
		}
	}
	e := &renderedPreview{key: key, rasterW: bucket}
	a.renderedPrev[mapKey] = e

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
	prefix := tileID + "\x00"
	for mk, e := range a.renderedPrev {
		if !strings.HasPrefix(mk, prefix) {
			continue
		}
		if e.url != "" {
			js.Global().Get("URL").Call("revokeObjectURL", e.url)
		}
		delete(a.renderedPrev, mk)
	}
}
