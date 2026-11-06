//go:build js && wasm

package main

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/markdown"
	"github.com/josephburnett/gridwell/client/preview"
	"github.com/josephburnett/gridwell/client/rasterprev"
	"github.com/josephburnett/gridwell/client/textedit"
)

// Rendered grid previews: a text tile whose stored text_mode is "rendered"
// previews as the rendered document, so how you leave a tile is how it
// presents from outside. markdown.RenderHTML stays the one renderer, and this
// rasterizes its output through an SVG foreignObject image, which holds no
// form control (markdown.PictureXHTML). Rasterization is async, so raw source
// paints until the tile's first image decodes. rasterprev.Cache owns every
// caching decision, and preview.LoadImage the image load.

// renderedPreviewMaxH caps the rasterized document height in CSS px. Beyond
// it a preview falls back to raw source: previews are a glance, not a
// reader.
const renderedPreviewMaxH = 4000.0

// svgRasterizer is rasterprev.Rasterizer's only implementation.
type svgRasterizer struct{}

// Rasterize loads svg through preview.LoadImage.
func (svgRasterizer) Rasterize(svg string, onReady func(rasterprev.Raster), onError func()) {
	blob := js.Global().Get("Blob").New(js.ValueOf([]any{svg}), js.ValueOf(map[string]any{"type": "image/svg+xml"}))
	preview.LoadImage(blob, func(img *preview.JSImage) { onReady(&svgRaster{img, svg}) }, onError)
}

// svgRaster is a loaded SVG image and the svg it was made of, for
// paintedText.
type svgRaster struct {
	*preview.JSImage
	svg string
}

// drawRenderedPreview crops tile n's raster into the box (see
// markdown.PreviewRasterDraw), kicking an async rasterization on a miss. The
// raster is made in the device px the frame draws it in. While it runs,
// rasterprev.Cache answers with a neighbouring width's raster; false means it
// has none, and the caller paints raw source.
func (a *App) drawRenderedPreview(n *gridwellv1.Tile, frame markdown.PreviewFrame,
	x, y, w, h, topInset float64) bool {
	bucket := rasterprev.Bucket(frame.ContentW)
	k := rasterprev.Key{
		TileID: n.Id,
		Bytes:  a.c.BodyGen(rpc.ContentID(n)),
		Bucket: bucket,
		Scale:  frame.Scale * a.win.Get("devicePixelRatio").Float(),
		Org:    markdown.IsOrg(n.AltText),
		Theme:  a.themeName.String(),
	}
	r, made, _ := a.views.renderedPrev.Ensure(k, func() (string, bool) {
		body, ok := a.tileBody(n)
		if !ok {
			return "", false // blob fetch in flight; the raw path warms it too
		}
		// The SVG foreignObject is an XML context, so serialize through the
		// DOM to make goldmark's HTML5 output well-formed.
		div := a.doc.Call("createElement", "div")
		div.Set("innerHTML", textedit.PresentationHTML(n, body))
		xhtml := js.Global().Get("XMLSerializer").New().Call("serializeToString", div).String()
		return markdown.PreviewSVG(xhtml, bucket, renderedPreviewMaxH, k.Scale, a.pal), true
	}, func() { a.draw() })
	sr, ok := r.(*svgRaster)
	if !ok {
		return false
	}
	a.e2e.lastRasterSVG = sr.svg
	d, ok := markdown.PreviewRasterDraw(frame, made.Bucket, renderedPreviewMaxH, made.Scale, x, y, w, h, topInset)
	if !ok {
		return false
	}
	a.cctx.Call("drawImage", sr.Val(), d.SX, d.SY, d.SW, d.SH, d.DX, d.DY, d.DW, d.DH)
	a.noteTextPreview(n.Id, &d, made.Bucket)
	return true
}

// textPreview is e2e attribution, read by the textPreviews testhook: the last preview
// a text tile painted, and how often raw source painted after a raster had.
type textPreview struct {
	draw           *markdown.RasterDraw // nil for raw source
	rasterW        float64
	sawRaster      bool
	rawAfterRaster int
}

func (a *App) noteTextPreview(tileID string, d *markdown.RasterDraw, rasterW float64) {
	f := a.e2e.textPreviews[tileID]
	f.draw, f.rasterW = d, rasterW
	if d != nil {
		f.sawRaster = true
	} else if f.sawRaster {
		f.rawAfterRaster++
	}
	a.e2e.textPreviews[tileID] = f
}

// renderedRasterFailed is rasterprev.Cache's verdict on a document that never
// became a picture. The cache settles that key, so this fires once per
// (tile, version, width bucket) rather than once per draw.
func (a *App) renderedRasterFailed(tileID string) {
	a.reportErr(errsurface.Error, "rendered-preview:"+tileID,
		"rendered preview could not be drawn — showing source")
}
