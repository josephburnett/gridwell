//go:build js && wasm

package main

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/markdown"
	"github.com/josephburnett/gridwell/client/rasterprev"
	"github.com/josephburnett/gridwell/client/textedit"
)

// Rendered grid previews: a text tile whose stored text_mode is "rendered"
// previews as the rendered document, so how you leave a tile is how it
// presents from outside. markdown.RenderHTML stays the one renderer, and this
// rasterizes its output through an SVG foreignObject image. Rasterization is
// async, so raw source paints until the tile's first image decodes.
// rasterprev.Cache owns
// every caching decision; this file is the blob-and-Image glue.

// renderedPreviewMaxH caps the rasterized document height in CSS px. Beyond
// it a preview falls back to raw source: previews are a glance, not a
// reader.
const renderedPreviewMaxH = 4000.0

// svgRasterizer is rasterprev.Rasterizer's only implementation.
type svgRasterizer struct{}

// Rasterize allocates an onload and an onerror js.Func, both released once
// either fires.
func (svgRasterizer) Rasterize(svg string, onReady func(rasterprev.Raster), onError func()) {
	blob := js.Global().Get("Blob").New(
		js.ValueOf([]any{svg}), js.ValueOf(map[string]any{"type": "image/svg+xml"}))
	url := js.Global().Get("URL").Call("createObjectURL", blob).String()
	img := js.Global().Get("Image").New()
	var onload, onerror js.Func
	release := func() { onload.Release(); onerror.Release() }
	onload = js.FuncOf(func(js.Value, []js.Value) any {
		release()
		onReady(&svgRaster{img: img, url: url})
		return nil
	})
	onerror = js.FuncOf(func(js.Value, []js.Value) any {
		release()
		js.Global().Get("URL").Call("revokeObjectURL", url)
		onError()
		return nil
	})
	img.Set("onload", onload)
	img.Set("onerror", onerror)
	img.Set("src", url)
}

// svgRaster wraps a loaded HTMLImageElement and the object URL behind it.
type svgRaster struct {
	img     js.Value
	url     string
	revoked bool
}

// Truthy is false after Revoke, the object URL being gone.
func (r *svgRaster) Truthy() bool { return r != nil && !r.revoked && r.img.Truthy() }

// Revoke releases the createObjectURL. It is idempotent.
func (r *svgRaster) Revoke() {
	if r == nil || r.revoked {
		return
	}
	r.revoked = true
	js.Global().Get("URL").Call("revokeObjectURL", r.url)
}

// renderedRasterFor returns a loaded raster for tile n near logical width
// contentW and the width it was made at, kicking an async rasterization on a
// miss. While that runs, rasterprev.Cache answers with a neighbouring width's
// raster; ok is false only when it has none, and the caller paints raw source.
func (a *App) renderedRasterFor(n *gridwellv1.Tile, contentW float64) (js.Value, float64, bool) {
	bucket := rasterprev.Bucket(contentW)
	k := rasterprev.Key{
		TileID: n.Id,
		Bytes:  a.c.ContentStamp(rpc.ContentID(n)),
		Bucket: bucket,
		Org:    markdown.IsOrg(n.AltText),
		Theme:  a.themeName.String(),
	}
	r, madeAt, ok := a.views.renderedPrev.Ensure(k, func() (string, bool) {
		body, ok := a.tileBody(n)
		if !ok {
			return "", false // blob fetch in flight; the raw path warms it too
		}
		// The SVG foreignObject is an XML context, so serialize through the
		// DOM to make goldmark's HTML5 output well-formed.
		div := a.doc.Call("createElement", "div")
		div.Set("innerHTML", textedit.PresentationHTML(n, body))
		xhtml := js.Global().Get("XMLSerializer").New().Call("serializeToString", div).String()
		return markdown.PreviewSVG(xhtml, bucket, renderedPreviewMaxH, a.pal), true
	}, func() { a.draw() })
	if !ok {
		return js.Value{}, 0, false
	}
	sr, ok := r.(*svgRaster)
	if !ok {
		return js.Value{}, 0, false
	}
	return sr.img, madeAt, true
}

// drawRenderedPreview windows the tile's raster into the box; see
// markdown.PreviewRasterDraw. False means the caller paints the raw fallback.
func (a *App) drawRenderedPreview(n *gridwellv1.Tile, frame markdown.PreviewFrame,
	x, y, w, h, topInset float64) bool {
	img, rasterW, ok := a.renderedRasterFor(n, frame.ContentW)
	if !ok {
		return false
	}
	d, ok := markdown.PreviewRasterDraw(frame, rasterW, renderedPreviewMaxH, x, y, w, h, topInset)
	if !ok {
		return false
	}
	a.cctx.Call("drawImage", img, d.SX, d.SY, d.SW, d.SH, d.DX, d.DY, d.DW, d.DH)
	a.noteTextFace(n.Id, &d, rasterW)
	return true
}

// textFace is e2e attribution, read by the textFaces testhook: the last face
// a text tile painted, and how often raw source painted after a raster had.
type textFace struct {
	draw           *markdown.RasterDraw // nil for raw source
	rasterW        float64
	sawRaster      bool
	rawAfterRaster int
}

func (a *App) noteTextFace(tileID string, d *markdown.RasterDraw, rasterW float64) {
	f := a.textFaces[tileID]
	f.draw, f.rasterW = d, rasterW
	if d != nil {
		f.sawRaster = true
	} else if f.sawRaster {
		f.rawAfterRaster++
	}
	a.textFaces[tileID] = f
}

// renderedRasterFailed is rasterprev.Cache's verdict on a document that never
// became a picture. The cache settles that key, so this fires once per
// (tile, version, width bucket) rather than once per frame.
func (a *App) renderedRasterFailed(tileID string) {
	a.reportErr(errsurface.Error, "rendered-preview:"+tileID,
		"rendered preview could not be drawn — showing source")
}
