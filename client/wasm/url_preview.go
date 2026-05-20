//go:build js && wasm

package main

import (
	"sync"
	"syscall/js"

	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// Live-tab presence is no longer modeled as tile state. A URL tile in
// the grid view always shows its cached preview JPEG; that preview is
// written when a descended client disconnects (see server/url_stream.go
// closeSession). Right-click on a URL tile means fork-on-drag-release,
// nothing else.

// drawURLTileInPane renders a URL tile that's currently the pane's
// FileFocus (i.e., the user descended into it). The pane's
// inner-rect (x, y, w, h) gets the cached preview image scaled to fit.
// While the WebSocket stream is open, frames flow into the same
// urlPreview cache, so this draw call automatically reflects them.
//
// If the stream has been lost (server-side Chromium died), an overlay
// is drawn on top of the last cached frame so the user knows the page
// is no longer interactive — without auto-reloading and silently
// throwing away their in-page state.
func (a *App) drawURLTileInPane(p *pane.Pane, n *rpc.Tile, x, y, w, h float64) {
	// Keep the server-side Chromium viewport in step with the area
	// we're painting into. notifyURLStreamSize is a no-op when the
	// size hasn't changed, so this is cheap to call every frame.
	a.notifyURLStreamSize(p.ID, int64(w), int64(h))

	a.cctx.Call("save")
	a.cctx.Call("beginPath")
	a.cctx.Call("rect", x, y, w, h)
	a.cctx.Call("clip")

	a.cctx.Set("fillStyle", colorFileInnerBg)
	a.cctx.Call("fillRect", x, y, w, h)

	if img, ok := a.urlPreview.Get(n.ID); ok {
		a.cctx.Call("drawImage", img, x, y, w, h)
	} else {
		a.fetchURLPreview(n.ID)
		a.cctx.Set("fillStyle", colorMuted)
		a.cctx.Set("font", "16px monospace")
		a.cctx.Call("fillText", n.URLString, x+16, y+32, w-32)
	}

	if a.urlStreamLost[p.ID] {
		a.cctx.Set("fillStyle", "rgba(0,0,0,0.55)")
		a.cctx.Call("fillRect", x, y, w, h)
		a.cctx.Set("fillStyle", "#f0c674")
		a.cctx.Set("font", "16px sans-serif")
		a.cctx.Set("textAlign", "center")
		a.cctx.Call("fillText", "page no longer active", x+w/2, y+h/2, w-32)
		a.cctx.Set("fillStyle", colorMuted)
		a.cctx.Set("font", "13px sans-serif")
		a.cctx.Call("fillText", "press Esc to ascend", x+w/2, y+h/2+22, w-32)
		a.cctx.Set("textAlign", "start")
	}

	a.cctx.Call("restore")
}

// drawURLTile renders a URL tile in the parent grid view. Layers:
//   1. dark-grey background (matches the file inner-bg used elsewhere)
//   2. the cached preview JPEG cropped to the tile footprint, or a
//      placeholder showing the URL text if no preview is loaded yet
//   3. the tile outline + selection highlight
func (a *App) drawURLTile(n *rpc.Tile, x, y, w, h float64, selected bool) {
	a.cctx.Call("save")
	a.cctx.Call("beginPath")
	a.cctx.Call("rect", x, y, w, h)
	a.cctx.Call("clip")

	a.cctx.Set("fillStyle", colorFileInnerBg)
	a.cctx.Call("fillRect", x, y, w, h)

	if img, ok := a.urlPreview.Get(n.ID); ok {
		a.cctx.Call("drawImage", img, x, y, w, h)
	} else {
		if w > 20 && h > 20 {
			a.cctx.Set("fillStyle", colorMuted)
			a.cctx.Set("font", "12px monospace")
			a.cctx.Call("fillText", n.URLString, x+8, y+18, w-16)
		}
		a.fetchURLPreview(n.ID)
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
