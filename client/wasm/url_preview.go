//go:build js && wasm

package main

import (
	"math"
	"sync"
	"syscall/js"

	"github.com/josephburnett/gridwell/client/dragdrop"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// drawURLTileInPane renders a URL tile that's currently the pane's
// FileFocus (i.e., the user descended into it). The pane's
// inner-rect (x, y, w, h) gets the cached preview image scaled to
// fit. While the WebSocket stream is open, frames flow into the same
// urlPreview cache, so this draw call automatically reflects them.
//
// The dormant case (no stream open) also lands here: it renders the
// frozen preview blob with a "wake up" hint overlay so the user
// knows the right-click gesture brings it back to life.
func (a *App) drawURLTileInPane(p *pane.Pane, n *rpc.Tile, x, y, w, h float64) {
	a.cctx.Call("save")
	a.cctx.Call("beginPath")
	a.cctx.Call("rect", x, y, w, h)
	a.cctx.Call("clip")

	a.cctx.Set("fillStyle", colorFileInnerBg)
	a.cctx.Call("fillRect", x, y, w, h)

	if img, ok := a.urlPreview.Get(n.ID); ok {
		a.cctx.Call("drawImage", img, x, y, w, h)
	} else {
		// No preview cached yet; fetch and show URL text in the
		// meantime.
		a.fetchURLPreview(n.ID)
		a.cctx.Set("fillStyle", colorMuted)
		a.cctx.Set("font", "16px monospace")
		a.cctx.Call("fillText", n.URLString, x+16, y+32, w-32)
	}

	if !n.Live {
		// Dormant: subtle hint that right-click wakes it.
		a.cctx.Set("fillStyle", "rgba(0,0,0,0.35)")
		a.cctx.Call("fillRect", x, y, w, h)
		a.cctx.Set("fillStyle", colorMenuItemHi)
		a.cctx.Set("font", "14px sans-serif")
		a.cctx.Call("fillText", "right-click to wake", x+16, y+h-16, w-32)
	}
	a.cctx.Call("restore")
	_ = p // pane parameter kept for symmetry with drawMarkdownInPane
}

// drawURLTile renders a URL tile in the parent grid view. Layers,
// bottom-up:
//   1. dark-grey background (matches the file inner-bg used elsewhere)
//   2. the cached preview JPEG cropped to the tile footprint, or a
//      placeholder showing the URL text if no preview is loaded yet
//   3. a small green dot in the upper-right if the tile is currently
//      live (Chromium tab present), giving the user the "live vs
//      dormant" cue called for in spec §8.3
//   4. the tile outline + selection highlight
func (a *App) drawURLTile(n *rpc.Tile, x, y, w, h float64, selected bool) {
	a.cctx.Call("save")
	a.cctx.Call("beginPath")
	a.cctx.Call("rect", x, y, w, h)
	a.cctx.Call("clip")

	a.cctx.Set("fillStyle", colorFileInnerBg)
	a.cctx.Call("fillRect", x, y, w, h)

	if img, ok := a.urlPreview.Get(n.ID); ok {
		// drawImage(image, dx, dy, dw, dh) — scale to the tile footprint.
		a.cctx.Call("drawImage", img, x, y, w, h)
	} else {
		// No preview yet: show the URL string as a small label so the
		// tile is at least identifiable, and kick off a fetch.
		if w > 20 && h > 20 {
			a.cctx.Set("fillStyle", colorMuted)
			a.cctx.Set("font", "12px monospace")
			a.cctx.Call("fillText", n.URLString, x+8, y+18, w-16)
		}
		a.fetchURLPreview(n.ID)
	}

	if n.Live {
		// Pulsing dot: scale-by-time gives the user a subtle motion
		// cue without burning CPU on full-frame animation.
		now := js.Global().Get("Date").Call("now").Float() / 1000.0
		pulse := 0.7 + 0.3*math.Sin(now*2*math.Pi/1.2) // 1.2s period
		radius := 4.0 * pulse
		cx := x + w - 8
		cy := y + 8
		a.cctx.Set("fillStyle", "#5ad15a")
		a.cctx.Call("beginPath")
		a.cctx.Call("arc", cx, cy, radius, 0.0, 2*math.Pi)
		a.cctx.Call("fill")
	}

	a.cctx.Set("strokeStyle", colorTextLine)
	a.cctx.Set("lineWidth", 1.0)
	a.cctx.Call("strokeRect", x, y, w, h)
	if selected {
		a.cctx.Set("strokeStyle", colorSelected)
		a.cctx.Set("lineWidth", 2.0)
		a.cctx.Call("strokeRect", x-1, y-1, w+2, h+2)
		a.cctx.Set("lineWidth", 1.0)
	}
	a.cctx.Call("restore")
}

// urlPreviewCache holds decoded URL-tile previews as browser
// HTMLImageElement values keyed by tile id, plus the object URL each
// image was loaded from so we can revoke it on replacement.
//
// JPEG decoding is offloaded to the browser via the Blob +
// URL.createObjectURL + new Image() chain — Image.decode is
// asynchronous, so callers register an onReady callback that fires
// when the image is renderable.
type urlPreviewCache struct {
	mu       sync.Mutex
	images   map[int64]js.Value // tile id → HTMLImageElement (when loaded)
	urls     map[int64]string   // tile id → object URL (for revocation)
	pending  map[int64]bool     // a fetch is in flight
	enqueued map[int64]bool     // a Put is decoding
}

func newURLPreviewCache() *urlPreviewCache {
	return &urlPreviewCache{
		images:   map[int64]js.Value{},
		urls:     map[int64]string{},
		pending:  map[int64]bool{},
		enqueued: map[int64]bool{},
	}
}

// Get returns the decoded image for tile id and whether it's ready
// to draw. Callers must check Truthy on the returned value.
func (c *urlPreviewCache) Get(tileID int64) (js.Value, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	img, ok := c.images[tileID]
	if !ok || !img.Truthy() {
		return js.Value{}, false
	}
	return img, true
}

// Pending reports whether a fetch or decode for tileID is in flight.
// Drawing code uses this to skip the "no preview yet" placeholder
// while the bytes are on the wire.
func (c *urlPreviewCache) Pending(tileID int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pending[tileID] || c.enqueued[tileID]
}

// MarkPending flags that a network fetch is in flight for tileID.
// Returns false if a fetch was already in flight (caller should not
// duplicate).
func (c *urlPreviewCache) MarkPending(tileID int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending[tileID] {
		return false
	}
	c.pending[tileID] = true
	return true
}

// ClearPending unsets the in-flight flag for tileID. Called after the
// fetch completes (success or failure).
func (c *urlPreviewCache) ClearPending(tileID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.pending, tileID)
}

// Invalidate drops the cached image for tileID (revoking its object
// URL) so the next render forces a refetch. Used when a
// `url_preview_updated` Subscribe event arrives.
func (c *urlPreviewCache) Invalidate(tileID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if u, ok := c.urls[tileID]; ok {
		js.Global().Get("URL").Call("revokeObjectURL", u)
		delete(c.urls, tileID)
	}
	delete(c.images, tileID)
}

// Put decodes a JPEG payload into an HTMLImageElement and stores it
// under tileID. onReady fires (on the JS thread) once the image
// finishes decoding and the cache entry is populated. Idempotent
// against the same in-flight decode (subsequent Puts overwrite the
// pending one).
func (c *urlPreviewCache) Put(tileID int64, jpegBytes []byte, onReady func()) {
	if len(jpegBytes) == 0 {
		return
	}
	c.mu.Lock()
	c.enqueued[tileID] = true
	c.mu.Unlock()

	// Copy the bytes into a JS Uint8Array.
	u8 := js.Global().Get("Uint8Array").New(len(jpegBytes))
	js.CopyBytesToJS(u8, jpegBytes)
	blobOpts := js.Global().Get("Object").New()
	blobOpts.Set("type", "image/jpeg")
	parts := js.Global().Get("Array").New()
	parts.Call("push", u8)
	blob := js.Global().Get("Blob").New(parts, blobOpts)
	objectURL := js.Global().Get("URL").Call("createObjectURL", blob).String()

	img := js.Global().Get("Image").New()
	var onloadFn, onerrorFn js.Func
	onloadFn = js.FuncOf(func(this js.Value, args []js.Value) any {
		c.mu.Lock()
		if old, ok := c.urls[tileID]; ok {
			js.Global().Get("URL").Call("revokeObjectURL", old)
		}
		c.images[tileID] = img
		c.urls[tileID] = objectURL
		delete(c.enqueued, tileID)
		c.mu.Unlock()
		onloadFn.Release()
		onerrorFn.Release()
		if onReady != nil {
			onReady()
		}
		return nil
	})
	onerrorFn = js.FuncOf(func(this js.Value, args []js.Value) any {
		js.Global().Get("URL").Call("revokeObjectURL", objectURL)
		c.mu.Lock()
		delete(c.enqueued, tileID)
		c.mu.Unlock()
		onloadFn.Release()
		onerrorFn.Release()
		return nil
	})
	img.Set("onload", onloadFn)
	img.Set("onerror", onerrorFn)
	img.Set("src", objectURL)
}

// toggleURLLiveness calls WakeURL or CaptureURL depending on the
// tile's current Live flag. The server's response (and any follow-up
// Subscribe events) drive the cache update; this function only fires
// the RPC. Spec §8.3: right-click on a URL tile body (no drag)
// toggles wake/capture.
func (a *App) toggleURLLiveness(rd *rightDragState, n *rpc.Tile) {
	p := a.tree.FindPane(rd.tilePaneID)
	if p == nil {
		return
	}
	pscreen := dragdrop.Pane{
		ScreenX: rd.tilePaneR.X, ScreenY: rd.tilePaneR.Y,
		ScreenW: rd.tilePaneR.W, ScreenH: rd.tilePaneR.H,
		Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom, CellPx: cellPx,
	}
	view := a.paneViewRect(p, pscreen)
	wasLive := n.Live
	gid := a.gridIDForPath(p.Path)
	tileID := n.ID

	go func() {
		var resp rpc.TileResponse
		var status int
		var err error
		if wasLive {
			req := rpc.CaptureURLRequest{
				Path:     rpc.Path{WellIDs: p.Path},
				ViewRect: view,
				TileID:   tileID,
			}
			status, err = postJSON("/rpc/CaptureURL", req, &resp)
		} else {
			req := rpc.WakeURLRequest{
				Path:     rpc.Path{WellIDs: p.Path},
				ViewRect: view,
				TileID:   tileID,
			}
			status, err = postJSON("/rpc/WakeURL", req, &resp)
		}
		if err != nil || status != 200 {
			return
		}
		// Update the cached tile so the live indicator changes
		// immediately, ahead of the Subscribe event arrival.
		a.c.UpdateTile(gid, resp.Tile)
		a.draw()
	}()
}

// forkURLDrop fires ForkURL with the source tile + the drop position
// computed from the release coordinates. Resolves the destination
// pane from the cursor; refuses (silently) if the drop is off-canvas
// or overlaps an existing tile.
func (a *App) forkURLDrop(rd *rightDragState, src *rpc.Tile, sx, sy float64) {
	srcPane := a.tree.FindPane(rd.tilePaneID)
	if srcPane == nil {
		return
	}
	srcPaneScreen := dragdrop.Pane{
		ScreenX: rd.tilePaneR.X, ScreenY: rd.tilePaneR.Y,
		ScreenW: rd.tilePaneR.W, ScreenH: rd.tilePaneR.H,
		Cx: srcPane.Cx, Cy: srcPane.Cy, Zoom: srcPane.Zoom, CellPx: cellPx,
	}
	srcView := a.paneViewRect(srcPane, srcPaneScreen)

	destPane, destRect, ok := a.paneAtScreen(sx, sy)
	if !ok {
		return
	}
	destPaneScreen := dragdrop.Pane{
		ScreenX: destRect.X, ScreenY: destRect.Y, ScreenW: destRect.W, ScreenH: destRect.H,
		Cx: destPane.Cx, Cy: destPane.Cy, Zoom: destPane.Zoom, CellPx: cellPx,
	}
	dcx, dcy := destPaneScreen.ScreenToCell(sx, sy)
	dropX := int64(math.Floor(dcx))
	dropY := int64(math.Floor(dcy))

	// Skip if drop overlaps an existing tile (server would reject
	// anyway, but we save the round-trip).
	if a.tileAtCell(destPane, dropX, dropY) != nil {
		return
	}

	destGridID := a.gridIDForPath(destPane.Path)
	destView := a.paneViewRect(destPane, destPaneScreen)
	tileID := src.ID
	go func() {
		var resp rpc.ForkURLResponse
		req := rpc.ForkURLRequest{
			Path:         rpc.Path{WellIDs: srcPane.Path},
			ViewRect:     srcView,
			TileID:       tileID,
			DestGridID:   destGridID,
			DestPath:     rpc.Path{WellIDs: destPane.Path},
			DestViewRect: destView,
			X:            dropX, Y: dropY,
		}
		status, err := postJSON("/rpc/ForkURL", req, &resp)
		if err != nil || status != 200 {
			return
		}
		// The Subscribe event for the new tile will land via SSE and
		// the cache will pick it up automatically. Trigger a redraw
		// to reflect the change immediately on success.
		a.fetchGrid(destGridID)
		a.draw()
	}()
}

// fetchURLPreview asynchronously requests the JPEG for the given URL
// tile, decodes it into the preview cache, and triggers a redraw on
// completion. Idempotent: short-circuits if already in flight or
// already cached.
func (a *App) fetchURLPreview(tileID int64) {
	if _, ok := a.urlPreview.Get(tileID); ok {
		return
	}
	if !a.urlPreview.MarkPending(tileID) {
		return
	}
	go func() {
		var resp rpc.GetTilePreviewResponse
		status, err := postJSON("/rpc/GetTilePreview", rpc.GetTilePreviewRequest{TileID: tileID}, &resp)
		a.urlPreview.ClearPending(tileID)
		if err != nil || status != 200 || len(resp.JPEG) == 0 {
			return
		}
		a.urlPreview.Put(tileID, resp.JPEG, func() { a.draw() })
	}()
}
