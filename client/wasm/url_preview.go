//go:build js && wasm

package main

import (
	"context"
	"syscall/js"

	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/preview"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// Live-tab presence is no longer modeled as tile state. A URL tile in
// the grid view always shows its cached preview JPEG; that preview is
// written when a descended client disconnects (see server/url_stream.go
// closeSession). Right-click on a URL tile means fork-on-drag-release,
// nothing else.

// drawImageCoverCentered draws img into the (x,y,w,h) rect using
// object-fit: cover semantics anchored to the center of the source
// image — the image is uniformly scaled so it fully covers the
// destination, and any overflow is cropped evenly from both sides
// (never stretched).
//
// No image bytes are discarded — this is purely a draw-time crop, so
// subsequent renders at a different destination aspect ratio (e.g.
// after a tile resize) crop differently from the same source frame.
func drawImageCoverCentered(c js.Value, img js.Value, x, y, w, h float64) {
	drawImageCover(c, img, x, y, w, h, 0, 0)
}

// drawImageCover draws img into (x,y,w,h) with cover semantics and an
// additional (panX, panY) offset applied in destination pixels. The pan
// shifts which portion of the scaled image is visible, clamped so the
// image always covers the full destination rect (no empty space).
//
// panX > 0 slides the image left (shows right portion); panY > 0 slides
// up (shows lower portion). Clamping is handled by the caller via
// clampURLPan — here we only apply the raw offset.
func drawImageCover(c js.Value, img js.Value, x, y, w, h, panX, panY float64) {
	iw := img.Get("naturalWidth").Float()
	ih := img.Get("naturalHeight").Float()
	// Cover source-rect + pan is the pure preview.CoverSrcRect; a degenerate
	// image/dest falls back to a stretch draw.
	sx, sy, sw, sh, ok := preview.CoverSrcRect(iw, ih, w, h, panX, panY)
	if !ok {
		c.Call("drawImage", img, x, y, w, h)
		return
	}
	c.Call("drawImage", img, sx, sy, sw, sh, x, y, w, h)
}

// clampURLPan clamps the frozen-descent pan so the cover-scaled image always
// fills the destination rect. Thin wrapper over the tested preview.ClampPan.
func clampURLPan(panX, panY, iw, ih, w, h float64) (float64, float64) {
	return preview.ClampPan(panX, panY, iw, ih, w, h)
}

// drawURLTileInPane renders a URL tile that's currently the pane's
// TextFocus (i.e., the user descended into it). The pane's
// inner-rect (x, y, w, h) gets the cached preview image in cover mode.
// While the WebSocket stream is open, frames flow into the same
// urlPreview cache, so this draw call automatically reflects them.
//
// Frozen descents apply pan (urlPanX/Y) so the user can drag to see the
// overflow. Live descents show the center crop (no pan — clicks forward
// to Chromium and the page manages its own viewport).
func (a *App) drawURLTileInPane(p *pane.Pane, n *rpc.Tile, x, y, w, h float64) {
	// When live, the native WebContentsView paints over this content box;
	// the JPEG drawn here is the fallback shown while the view is parked
	// during a gesture, and the frozen preview otherwise. Its bounds are
	// tracked by syncURLViews, not from this draw path.

	a.cctx.Call("save")
	a.cctx.Call("beginPath")
	a.cctx.Call("rect", x, y, w, h)
	a.cctx.Call("clip")

	a.cctx.Set("fillStyle", colorFileInnerBg)
	a.cctx.Call("fillRect", x, y, w, h)

	if cached, ok := a.urlPreview.Get(n.ID, n.PreviewBlobID); ok {
		if img, ok := previewImage(cached); ok {
			live := a.urlStreams[p.ID] != nil
			if live {
				// Live: center-crop, no pan — the page renders at pane size.
				drawImageCoverCentered(a.cctx, img, x, y, w, h)
			} else {
				// Frozen: cover + pan so the user can drag to see overflow.
				var panX, panY float64
				if pl, ok := a.localIf(p.ID); ok {
					panX, panY = pl.PanX, pl.PanY
				}
				iw := img.Get("naturalWidth").Float()
				ih := img.Get("naturalHeight").Float()
				panX, panY = clampURLPan(panX, panY, iw, ih, w, h)
				drawImageCover(a.cctx, img, x, y, w, h, panX, panY)
			}
		}
	} else {
		a.fetchURLPreview(n.ID, n.PreviewBlobID)
		a.cctx.Set("fillStyle", colorMuted)
		a.cctx.Set("font", "16px monospace")
		a.cctx.Call("fillText", n.URLString, x+16, y+32, w-32)
	}

	a.cctx.Call("restore")
}

// drawShellTileInPane renders a shell tile that's currently the pane's
// TextFocus (the user descended into it). Mirrors drawURLTileInPane:
// the cached freeze-frame JPEG fills the pane in cover mode; when no
// preview is loaded yet the fetch is kicked off and a hint paints the
// stored cwd so the user sees *something* while the JPEG decodes.
//
// When a live shell stream is attached to the pane, the xterm.js DOM
// overlay sits on top of this canvas — the JPEG underneath becomes
// invisible, but painting it costs ~nothing and avoids a flash if the
// overlay hasn't been positioned yet for the current frame.
func (a *App) drawShellTileInPane(p *pane.Pane, n *rpc.Tile, x, y, w, h float64) {
	a.cctx.Call("save")
	a.cctx.Call("beginPath")
	a.cctx.Call("rect", x, y, w, h)
	a.cctx.Call("clip")

	a.cctx.Set("fillStyle", colorShellFill)
	a.cctx.Call("fillRect", x, y, w, h)

	if cached, ok := a.urlPreview.Get(n.ID, n.PreviewBlobID); ok {
		if img, ok := previewImage(cached); ok {
			drawImageCoverCentered(a.cctx, img, x, y, w, h)
		}
	} else if n.PreviewBlobID != 0 {
		a.fetchURLPreview(n.ID, n.PreviewBlobID)
	} else if !a.hasShellStream(p.ID) {
		// No preview yet, no live stream — pre-refresh state. Show
		// the shell glyph so the descent reads as a frozen shell
		// rather than a blank box.
		drawShellGlyph(a.cctx, x, y, w, h, colorShellBorder)
	}

	a.cctx.Call("restore")
}

// drawShellTile renders a shell tile in the parent grid view. Same
// pattern as drawURLTile: cached JPEG covers the cell when available,
// otherwise a placeholder shows the cwd path on a dark fill. The outline
// is the shell orange — bash runs outside Gridwell's data world. Reuses
// urlPreview as the JPEG cache; the cache is keyed by tile id so URL and
// shell tiles can share a single decode pool.
func (a *App) drawShellTile(n *rpc.Tile, x, y, w, h float64, selected bool) {
	a.cctx.Call("save")
	a.cctx.Call("beginPath")
	a.cctx.Call("rect", x, y, w, h)
	a.cctx.Call("clip")

	a.cctx.Set("fillStyle", colorShellFill)
	a.cctx.Call("fillRect", x, y, w, h)

	if cached, ok := a.urlPreview.Get(n.ID, n.PreviewBlobID); ok {
		if img, ok := previewImage(cached); ok {
			drawImageCoverCentered(a.cctx, img, x, y, w, h)
		}
	} else if n.PreviewBlobID != 0 {
		a.fetchURLPreview(n.ID, n.PreviewBlobID)
	} else if w > 20 && h > 20 {
		// No preview yet (palette drop never refreshed) — paint the
		// shell glyph so the swatch reads as a shell rather than a
		// blank box.
		drawShellGlyph(a.cctx, x, y, w, h, colorShellBorder)
	}

	strokeTileBorder(a.cctx, x, y, w, h, colorShellBorder, tileBorderPx)
	if selected {
		drawSelectedTileOutline(a.cctx, x, y, w, h)
	}
	a.cctx.Call("restore")
}

// drawURLTile renders a URL tile in the parent grid view. Layers:
//  1. dark-grey background (matches the file inner-bg used elsewhere)
//  2. the cached preview JPEG cropped to the tile footprint, or a
//     placeholder showing the URL text if no preview is loaded yet
//  3. the tile outline + selection highlight
func (a *App) drawURLTile(n *rpc.Tile, x, y, w, h float64, selected bool) {
	a.cctx.Call("save")
	a.cctx.Call("beginPath")
	a.cctx.Call("rect", x, y, w, h)
	a.cctx.Call("clip")

	a.cctx.Set("fillStyle", colorFileInnerBg)
	a.cctx.Call("fillRect", x, y, w, h)

	if cached, ok := a.urlPreview.Get(n.ID, n.PreviewBlobID); ok {
		if img, ok := previewImage(cached); ok {
			drawImageCoverCentered(a.cctx, img, x, y, w, h)
		}
	} else {
		if w > 20 && h > 20 {
			a.cctx.Set("fillStyle", colorMuted)
			a.cctx.Set("font", "12px monospace")
			a.cctx.Call("fillText", n.URLString, x+8, y+18, w-16)
		}
		a.fetchURLPreview(n.ID, n.PreviewBlobID)
	}

	strokeTileBorder(a.cctx, x, y, w, h, colorURLLine, tileBorderPx)
	if selected {
		drawSelectedTileOutline(a.cctx, x, y, w, h)
	}
	a.cctx.Call("restore")
}

// previewImage is the wasm-side cast: the cache stores a
// preview.Image interface, but the canvas drawing helpers want a raw
// HTMLImageElement (js.Value) to hand to drawImage. Every Get
// callsite funnels through this helper so the cast lives in one place.
// Returns false when the entry's underlying type isn't a *preview.JSImage
// (defensive; the only Decoder registered with this cache produces
// JSImage values).
func previewImage(img preview.Image) (js.Value, bool) {
	ji, ok := img.(*preview.JSImage)
	if !ok || ji == nil {
		return js.Value{}, false
	}
	return ji.Val(), true
}

// fetchURLPreview asynchronously requests the JPEG for the given
// tile, decodes it into the preview cache, and triggers a redraw on
// completion. Idempotent: short-circuits if a fetch is already in
// flight, or if a cached entry is already valid for blobID. blobID
// is the tile's current PreviewBlobID — passed through so the cache
// can detect a server-side update and re-fetch on the next call.
func (a *App) fetchURLPreview(tileID string, blobID int64) {
	if blobID == 0 {
		return
	}
	if _, ok := a.urlPreview.Get(tileID, blobID); ok {
		return
	}
	if !a.urlPreview.MarkFetching(tileID) {
		return
	}
	go func() {
		jpeg, err := a.cl.GetTilePreview(context.Background(), tileID)
		a.urlPreview.ClearFetching(tileID)
		if err != nil || len(jpeg) == 0 {
			return
		}
		a.urlPreview.Put(tileID, blobID, jpeg, func() { a.draw() })
	}()
}
