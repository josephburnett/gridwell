//go:build js && wasm

package main

import (
	"context"
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/preview"
)

// Live-tab presence is no longer modeled as tile state. A URL tile in
// the grid view always shows its cached preview JPEG, written back at the
// ascent freeze.

// drawImageContain draws img into the (x,y,w,h) rect using object-fit:
// contain semantics: black bars fill the rect, then the image is uniformly
// scaled to FIT and centered — bars remain on at most one axis. The owner's
// call (issue #89): a preview is always shown whole, never cover-cropped, so
// radically different aspect ratios still read as what they are. The single
// wasm chokepoint every raster preview draw flows through.
func drawImageContain(c js.Value, img js.Value, x, y, w, h float64) {
	c.Set("fillStyle", "#000")
	c.Call("fillRect", x, y, w, h)
	iw := img.Get("naturalWidth").Float()
	ih := img.Get("naturalHeight").Float()
	// The fit rect is the pure preview.ContainDstRect; a degenerate
	// image/dest falls back to a stretch draw.
	dx, dy, dw, dh, ok := preview.ContainDstRect(iw, ih, x, y, w, h)
	if !ok {
		c.Call("drawImage", img, x, y, w, h)
		return
	}
	c.Call("drawImage", img, dx, dy, dw, dh)
}

// pagePreviewBlobID is the cache/fetch key for a serves_page tile's preview
// (2026-08-11). Page tiles have no preview_blob_id — the owning plugin
// DERIVES the frozen face from the content itself (fs: a thumbnail of the
// file), so there is no server-side generation counter to key freshness by.
// A fixed sentinel means: fetch once per session, keep until reload.
const pagePreviewBlobID = -1

// previewBlobKey resolves the urlPreview cache key for a tile: the stored
// preview blob id when there is one, the page sentinel for a serves_page
// tile, 0 (= no preview, no fetch) otherwise. The one keying rule for every
// preview draw and fetch.
func previewBlobKey(n *rpc.Tile) int64 {
	if n.PreviewBlobID != 0 {
		return n.PreviewBlobID
	}
	if n.ServesPage {
		return pagePreviewBlobID
	}
	return 0
}

// drawURLTileInPane renders a URL tile that is the pane's current place
// (i.e., the user descended into it). The pane's inner-rect
// (x, y, w, h) gets the cached preview image letterboxed to fit. While a
// live view is attached, mirror frames flow into the same urlPreview cache, so
// this draw call automatically reflects them. (The old frozen-descent
// cover-crop pan is gone with cover mode itself — under fit there is no
// overflow to pan into.)
func (a *App) drawURLTileInPane(n *rpc.Tile, x, y, w, h float64) {
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

	if cached, ok := a.urlPreview.Get(n.ContentID(), previewBlobKey(n)); ok {
		if img, ok := previewImage(cached); ok {
			drawImageContain(a.cctx, img, x, y, w, h)
		}
	} else {
		a.fetchURLPreview(n.ContentID(), previewBlobKey(n))
		label := n.URLString
		if label == "" {
			label = n.AltText // a page tile has no address; its name says what it is
		}
		a.cctx.Set("fillStyle", colorMuted)
		a.cctx.Set("font", "16px monospace")
		a.cctx.Call("fillText", label, x+16, y+32, w-32)
	}

	a.cctx.Call("restore")
}

// drawPageTile renders a serves_page tile in the parent grid view (the
// text-kind arm of drawNodeWithPreview): the plugin-derived preview image
// letterboxed into the footprint — an image file looks like the image —
// inside the text family's border (it IS a file; only its presentation is
// web content). Falls back to the file name while the thumbnail loads (or
// when the plugin serves none).
func (a *App) drawPageTile(n *rpc.Tile, x, y, w, h float64, selected, outside, dashed bool) {
	a.cctx.Call("save")
	a.cctx.Call("beginPath")
	a.cctx.Call("rect", x, y, w, h)
	a.cctx.Call("clip")

	a.cctx.Set("fillStyle", colorFileInnerBg)
	a.cctx.Call("fillRect", x, y, w, h)

	if cached, ok := a.urlPreview.Get(n.ContentID(), previewBlobKey(n)); ok {
		if img, ok := previewImage(cached); ok {
			drawImageContain(a.cctx, img, x, y, w, h)
		}
	} else {
		if w > 20 && h > 20 {
			a.cctx.Set("fillStyle", colorMuted)
			a.cctx.Set("font", "12px monospace")
			a.cctx.Call("fillText", n.AltText, x+8, y+18, w-16)
		}
		a.fetchURLPreview(n.ContentID(), previewBlobKey(n))
	}

	line := colorMarkdownLine
	if outside {
		line = colorMarkdownLineFaded
	}
	if dashed {
		setTileDash(a.cctx)
	}
	strokeTileBorder(a.cctx, x, y, w, h, line, tileBorderPx)
	if dashed {
		clearTileDash(a.cctx)
	}
	if selected {
		drawSelectedTileOutline(a.cctx, x, y, w, h)
	}
	a.cctx.Call("restore")
}

// drawShellTileInPane renders a shell tile that is the pane's current place
// (the user descended into it). Mirrors drawURLTileInPane:
// the cached freeze-frame JPEG letterboxes into the pane; when no
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

	if cached, ok := a.urlPreview.Get(n.ContentID(), n.PreviewBlobID); ok {
		if img, ok := previewImage(cached); ok {
			// Stand-in geometry, not letterbox: the live xterm canvas sits
			// top-left at integer-cell size, so the snapshot must go back
			// exactly there — contain-fit centered and scaled it by the
			// leftover cell fraction, visibly shifting the terminal every
			// time the overlay parked (issue #224). shellStandinRect is the
			// one owner of this rect; the e2e hook reads the same function.
			if dx, dy, dw, dh, ok := a.shellStandinRect(img, x, y); ok {
				a.cctx.Call("drawImage", img, dx, dy, dw, dh)
			}
		}
	} else if n.PreviewBlobID != 0 {
		a.fetchURLPreview(n.ContentID(), n.PreviewBlobID)
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
func (a *App) drawShellTile(n *rpc.Tile, x, y, w, h float64, selected, dashed bool) {
	a.cctx.Call("save")
	a.cctx.Call("beginPath")
	a.cctx.Call("rect", x, y, w, h)
	a.cctx.Call("clip")

	a.cctx.Set("fillStyle", colorShellFill)
	a.cctx.Call("fillRect", x, y, w, h)

	if cached, ok := a.urlPreview.Get(n.ContentID(), n.PreviewBlobID); ok {
		if img, ok := previewImage(cached); ok {
			drawImageContain(a.cctx, img, x, y, w, h)
		}
	} else if n.PreviewBlobID != 0 {
		a.fetchURLPreview(n.ContentID(), n.PreviewBlobID)
	} else if w > 20 && h > 20 {
		// No preview yet (palette drop never refreshed) — paint the
		// shell glyph so the swatch reads as a shell rather than a
		// blank box.
		drawShellGlyph(a.cctx, x, y, w, h, colorShellBorder)
	}

	if dashed {
		setTileDash(a.cctx)
	}
	strokeTileBorder(a.cctx, x, y, w, h, colorShellBorder, tileBorderPx)
	if dashed {
		clearTileDash(a.cctx)
	}
	if selected {
		drawSelectedTileOutline(a.cctx, x, y, w, h)
	}
	a.cctx.Call("restore")
}

// drawURLTile renders a URL tile in the parent grid view. Layers:
//  1. dark-grey background (matches the file inner-bg used elsewhere)
//  2. the cached preview JPEG letterboxed into the tile footprint, or a
//     placeholder showing the URL text if no preview is loaded yet
//  3. the tile outline + selection highlight
func (a *App) drawURLTile(n *rpc.Tile, x, y, w, h float64, selected, dashed bool) {
	a.cctx.Call("save")
	a.cctx.Call("beginPath")
	a.cctx.Call("rect", x, y, w, h)
	a.cctx.Call("clip")

	a.cctx.Set("fillStyle", colorFileInnerBg)
	a.cctx.Call("fillRect", x, y, w, h)

	if cached, ok := a.urlPreview.Get(n.ContentID(), n.PreviewBlobID); ok {
		if img, ok := previewImage(cached); ok {
			drawImageContain(a.cctx, img, x, y, w, h)
		}
	} else {
		if w > 20 && h > 20 {
			a.cctx.Set("fillStyle", colorMuted)
			a.cctx.Set("font", "12px monospace")
			a.cctx.Call("fillText", n.URLString, x+8, y+18, w-16)
		}
		a.fetchURLPreview(n.ContentID(), n.PreviewBlobID)
	}

	if dashed {
		setTileDash(a.cctx)
	}
	strokeTileBorder(a.cctx, x, y, w, h, colorURLLine, tileBorderPx)
	if dashed {
		clearTileDash(a.cctx)
	}
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
	if a.urlPreview.KnownEmpty(tileID, blobID) {
		return // the server already answered "no preview" for this blob
	}
	if !a.urlPreview.MarkFetching(tileID) {
		return
	}
	go func() {
		jpeg, err := a.cl.GetTilePreview(context.Background(), tileID)
		a.urlPreview.ClearFetching(tileID)
		if err != nil {
			// A plugin that serves no previews answers Unimplemented — a
			// normal capability property (the tile shows its label), not a
			// failure. Anything else surfaces (charter §6).
			if !isUnimplemented(err) {
				a.surfaceRPCError("GetTilePreview", err)
				return // transient — the next draw may retry
			}
			a.urlPreview.PutEmpty(tileID, blobID)
			return
		}
		if len(jpeg) == 0 {
			// A completed answer settles the cache (PutEmpty) — leaving
			// the miss unrecorded re-fired this fetch on EVERY draw, one
			// RPC per non-decodable tile per frame, forever (#265).
			a.urlPreview.PutEmpty(tileID, blobID)
			return
		}
		a.urlPreview.Put(tileID, blobID, jpeg, func() { a.scheduleFrame() })
	}()
}

// shellStandinRect is the ONE owner of where a shell snapshot draws inside a
// pane: preview.StandinDstRect at the current device pixel ratio. The
// in-pane draw and the e2e testhook (thShellStandin) both read it, so the
// spec asserts the exact rect the renderer uses.
func (a *App) shellStandinRect(img js.Value, x, y float64) (dx, dy, dw, dh float64, ok bool) {
	dpr := a.win.Get("devicePixelRatio").Float()
	return preview.StandinDstRect(
		img.Get("naturalWidth").Float(), img.Get("naturalHeight").Float(),
		dpr, x, y)
}
