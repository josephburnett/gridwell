//go:build js && wasm

package main

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/clientsync"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/preview"
	"github.com/josephburnett/gridwell/client/traceevent"
)

// A URL tile in the grid view always shows its cached preview JPEG, written
// back at the ascent freeze. Live-tab presence is not tile state.

// drawImageContain draws img with object-fit: contain semantics. A preview is
// always shown whole, never cover-cropped, so a radically different aspect
// ratio still reads as what it is.
func (a *App) drawImageContain(c js.Value, img js.Value, x, y, w, h float64) {
	fillRectC(c, x, y, w, h, a.pal.PreviewLetterbox)
	iw := img.Get("naturalWidth").Float()
	ih := img.Get("naturalHeight").Float()
	// A degenerate image or dest falls back to a stretch draw.
	dx, dy, dw, dh, ok := preview.ContainDstRect(iw, ih, x, y, w, h)
	if !ok {
		c.Call("drawImage", img, x, y, w, h)
		return
	}
	c.Call("drawImage", img, dx, dy, dw, dh)
}

// drawPreviewFace paints a content tile's frozen face, running fallback when
// nothing is cached. One owner of "cached preview or stand-in", so no tile
// kind drifts into its own answer.
func (a *App) drawPreviewFace(n *gridwellv1.Tile, x, y, w, h float64, fill string, blobID int64, fallback func()) {
	fillRectC(a.cctx, x, y, w, h, fill)
	if cached, ok := a.views.urlPreview.Get(rpc.ContentID(n), blobID); ok {
		if img, ok := previewImage(cached); ok {
			a.drawImageContain(a.cctx, img, x, y, w, h)
		}
		return
	}
	fallback()
}

// drawPreviewPlaceholder paints the name a grid tile shows while its preview
// loads, and nothing on a tile too small to read a label.
func (a *App) drawPreviewPlaceholder(label string, x, y, w, h float64) {
	if w <= 20 || h <= 20 {
		return
	}
	drawLabel(a.cctx, label, x+8, y+18, labelOpts{
		font: "12px monospace", fill: a.pal.Muted, maxW: w - 16,
	})
}

// drawURLTileInPane renders the URL tile a pane is descended into. Mirror
// frames from a live view flow into the same urlPreview cache, so this draw
// reflects them.
func (a *App) drawURLTileInPane(n *gridwellv1.Tile, x, y, w, h float64) {
	// The native view paints over this box, so the JPEG here shows while it
	// is parked during a gesture. Bounds are syncURLViews'.
	withClip(a.cctx, x, y, w, h, func() {
		a.drawPreviewFace(n, x, y, w, h, a.pal.FileInnerBg, preview.BlobKey(n), func() {
			a.fetchURLPreview(rpc.ContentID(n), preview.BlobKey(n))
			label := urlTileLabel(n)
			drawLabel(a.cctx, label, x+16, y+32, labelOpts{
				font: "16px monospace", fill: a.pal.Muted, maxW: w - 32,
			})
		})
	})
}

// drawShellTileInPane is drawURLTileInPane's twin. A live xterm overlay sits
// on top of it, but painting underneath avoids a flash before the overlay is
// positioned for the frame.
func (a *App) drawShellTileInPane(p *pane.Pane, n *gridwellv1.Tile, x, y, w, h float64) {
	withClip(a.cctx, x, y, w, h, func() {
		fillRectC(a.cctx, x, y, w, h, a.pal.ShellFill)

		if cached, ok := a.views.urlPreview.Get(rpc.ContentID(n), n.PreviewBlobId); ok {
			if img, ok := previewImage(cached); ok {
				// Stand-in geometry, not letterbox: the live xterm canvas
				// sits top-left at integer-cell size, and contain-fit would
				// shift the terminal every time the overlay parks.
				if dx, dy, dw, dh, ok := a.shellStandinRect(img, x, y); ok {
					a.cctx.Call("drawImage", img, dx, dy, dw, dh)
				}
			}
		} else if n.PreviewBlobId != 0 {
			a.fetchURLPreview(rpc.ContentID(n), n.PreviewBlobId)
		} else if !a.hasShellStream(p.ID) {
			// No preview and no live stream: show the glyph so the descent
			// reads as a frozen shell rather than a blank box.
			drawShellGlyph(a.cctx, x, y, w, h, a.pal.ShellBorder)
		}
	})
}

// drawShellTile is drawURLTile for a shell. The outline is the shell orange,
// because bash runs outside Gridwell's data world.
func (a *App) drawShellTile(n *gridwellv1.Tile, x, y, w, h float64, selected, dashed bool) {
	withClip(a.cctx, x, y, w, h, func() {
		a.drawPreviewFace(n, x, y, w, h, a.pal.ShellFill, n.PreviewBlobId, func() {
			if n.PreviewBlobId != 0 {
				a.fetchURLPreview(rpc.ContentID(n), n.PreviewBlobId)
			} else if w > 20 && h > 20 {
				// A palette drop never refreshed, so paint the glyph rather
				// than a blank box.
				drawShellGlyph(a.cctx, x, y, w, h, a.pal.ShellBorder)
			}
		})

		a.strokeTileFrame(a.cctx, x, y, w, h, a.pal.ShellBorder, dashed, selected)
	})
}

// drawURLTile renders a URL tile in the parent grid view. A tile whose plugin
// serves its page has a face the plugin derives and no address to name; both
// ride the same two owners, preview.BlobKey and urlTileLabel.
func (a *App) drawURLTile(n *gridwellv1.Tile, x, y, w, h float64, selected, dashed bool) {
	withClip(a.cctx, x, y, w, h, func() {
		key := preview.BlobKey(n)
		a.drawPreviewFace(n, x, y, w, h, a.pal.FileInnerBg, key, func() {
			a.drawPreviewPlaceholder(urlTileLabel(n), x, y, w, h)
			a.fetchURLPreview(rpc.ContentID(n), key)
		})

		a.strokeTileFrame(a.cctx, x, y, w, h, a.pal.URLLine, dashed, selected)
	})
}

// urlTileLabel is what a url tile's face reads before its preview arrives: its
// address, or its name when the plugin serves the page and there is no address
// to show.
func urlTileLabel(n *gridwellv1.Tile) string {
	if n.UrlString != "" {
		return n.UrlString
	}
	return n.AltText
}

// previewImage is the one cast from the cache's preview.Image to the js.Value
// drawImage wants. False when the entry is not a *preview.JSImage, which this
// cache's only Decoder never produces.
func previewImage(img preview.Image) (js.Value, bool) {
	ji, ok := img.(*preview.JSImage)
	if !ok || ji == nil {
		return js.Value{}, false
	}
	return ji.Val(), true
}

// fetchURLPreview requests the JPEG for a tile and decodes it into the
// preview cache. blobID is the tile's current PreviewBlobId, so the cache
// detects a server-side update and re-fetches.
func (a *App) fetchURLPreview(tileID string, blobID int64) {
	if blobID == 0 {
		return
	}
	if _, ok := a.views.urlPreview.Get(tileID, blobID); ok {
		return
	}
	if a.views.urlPreview.KnownEmpty(tileID, blobID) {
		return // the server already answered "no preview" for this blob
	}
	// tileID is the content id, a leaf link's target, which may live in a
	// namespace this node no longer declares. Asking would put its verdict
	// on the error strip.
	if a.deadNamespace(tileID) {
		return
	}
	// The dedupe claim is client/inflight's and bounded, so a request the
	// network swallows cannot hold this tile's face for the life of the
	// page.
	ctx, done, ok := a.fetch.previewFetch.Begin(tileID)
	if !ok {
		return
	}
	go func() {
		defer done()
		jpeg, err := a.cl.GetTilePreview(ctx, tileID)
		// clientsync.ReactPreview is the one table; this runs its arms.
		r := clientsync.ReactPreview(err, len(jpeg) == 0)
		if r.Surface {
			a.surfaceRPCError("GetTilePreview", err)
		}
		if r.Settle {
			a.views.urlPreview.PutEmpty(tileID, blobID)
		}
		if r.Store {
			a.views.urlPreview.Put(tileID, blobID, jpeg, func() { a.scheduleFrame(traceevent.WhyPreview) })
		}
	}()
}

// shellStandinRect is the one owner of where a shell snapshot draws inside a
// pane. The in-pane draw and the e2e testhook both read it, so the spec
// asserts the exact rect the renderer uses.
func (a *App) shellStandinRect(img js.Value, x, y float64) (dx, dy, dw, dh float64, ok bool) {
	dpr := a.win.Get("devicePixelRatio").Float()
	return preview.StandinDstRect(
		img.Get("naturalWidth").Float(), img.Get("naturalHeight").Float(),
		dpr, x, y)
}

// previewDecodeFailed is preview.Cache's verdict on bytes that never became a
// picture. The cache settles that blob id, so this fires once per failed blob
// rather than once per frame.
func (a *App) previewDecodeFailed(tileID string) {
	a.reportErr(errsurface.Error, "preview:"+tileID, "preview image could not be decoded")
}
