//go:build js && wasm

package main

import (
	"context"
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
	if iw <= 0 || ih <= 0 || w <= 0 || h <= 0 {
		c.Call("drawImage", img, x, y, w, h)
		return
	}
	destAR := w / h
	imgAR := iw / ih
	var sw, sh float64
	if imgAR > destAR {
		sh = ih
		sw = ih * destAR
	} else {
		sw = iw
		sh = iw / destAR
	}
	// Center offset, then shift by pan (converted from dest-px to src-px).
	scale := sw / w // dest-px to source-px ratio
	sx := (iw-sw)/2 + panX*scale
	sy := (ih-sh)/2 + panY*scale
	c.Call("drawImage", img, sx, sy, sw, sh, x, y, w, h)
}

// clampURLPan returns (panX, panY) clamped so the image at cover scale
// always fills the destination rect — i.e., pan cannot expose empty
// space beyond the image edges. iw/ih are the image's natural pixel
// dimensions; w/h are the destination rect dimensions.
func clampURLPan(panX, panY, iw, ih, w, h float64) (float64, float64) {
	if iw <= 0 || ih <= 0 || w <= 0 || h <= 0 {
		return 0, 0
	}
	destAR := w / h
	imgAR := iw / ih
	var sw, sh float64
	if imgAR > destAR {
		sh = ih
		sw = ih * destAR
	} else {
		sw = iw
		sh = iw / destAR
	}
	// scale: how many source-px per dest-px
	scale := sw / w
	// Maximum pan in dest-px so we don't go past the image edges.
	// The center crop uses (iw-sw)/2 source px on each side, which is
	// (iw-sw)/(2*scale) dest-px of overflow available.
	maxPanX := (iw - sw) / (2 * scale)
	maxPanY := (ih - sh) / (2 * scale)
	if panX < -maxPanX {
		panX = -maxPanX
	}
	if panX > maxPanX {
		panX = maxPanX
	}
	if panY < -maxPanY {
		panY = -maxPanY
	}
	if panY > maxPanY {
		panY = maxPanY
	}
	return panX, panY
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
		live := a.urlStreams[p.ID] != nil
		if live {
			// Live: center-crop, no pan — the page renders at pane size.
			drawImageCoverCentered(a.cctx, img, x, y, w, h)
		} else {
			// Frozen: cover + pan so the user can drag to see overflow.
			panX := a.urlPanX[p.ID]
			panY := a.urlPanY[p.ID]
			iw := img.Get("naturalWidth").Float()
			ih := img.Get("naturalHeight").Float()
			panX, panY = clampURLPan(panX, panY, iw, ih, w, h)
			drawImageCover(a.cctx, img, x, y, w, h, panX, panY)
		}
	} else {
		a.fetchURLPreview(n.ID)
		a.cctx.Set("fillStyle", colorMuted)
		a.cctx.Set("font", "16px monospace")
		a.cctx.Call("fillText", n.URLString, x+16, y+32, w-32)
	}

	a.cctx.Call("restore")
}

// drawShellTile renders a shell tile in the parent grid view. Same
// pattern as drawURLTile: cached JPEG covers the cell when available,
// otherwise a placeholder shows the cwd path on a dark fill. The
// outline is the exit-red (shell tile lives in the red family — its
// contents come from outside Gridwell). Reuses urlPreview as the JPEG
// cache; the cache is keyed by tile id so URL and shell tiles can
// share a single decode pool.
func (a *App) drawShellTile(n *rpc.Tile, x, y, w, h float64, selected bool) {
	a.cctx.Call("save")
	a.cctx.Call("beginPath")
	a.cctx.Call("rect", x, y, w, h)
	a.cctx.Call("clip")

	a.cctx.Set("fillStyle", colorExitFill)
	a.cctx.Call("fillRect", x, y, w, h)

	if img, ok := a.urlPreview.Get(n.ID); ok {
		drawImageCoverCentered(a.cctx, img, x, y, w, h)
	} else if n.PreviewBlobID != 0 {
		a.fetchURLPreview(n.ID)
	} else if w > 20 && h > 20 {
		// No preview yet (palette drop never refreshed) — show the
		// stored cwd so the swatch reads as "shell in /tmp" rather
		// than a blank red box. The shell glyph is overlaid below.
		drawShellGlyph(a.cctx, x, y, w, h, colorExitBorder)
		if n.ShellCwd != "" {
			a.cctx.Set("fillStyle", colorMuted)
			a.cctx.Set("font", "10px monospace")
			a.cctx.Call("fillText", n.ShellCwd, x+6, y+h-6, w-12)
		}
	}

	strokeTileBorder(a.cctx, x, y, w, h, colorExitBorder, tileBorderPx)
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

	if img, ok := a.urlPreview.Get(n.ID); ok {
		drawImageCoverCentered(a.cctx, img, x, y, w, h)
	} else {
		if w > 20 && h > 20 {
			a.cctx.Set("fillStyle", colorMuted)
			a.cctx.Set("font", "12px monospace")
			a.cctx.Call("fillText", n.URLString, x+8, y+18, w-16)
		}
		a.fetchURLPreview(n.ID)
	}

	strokeTileBorder(a.cctx, x, y, w, h, colorURLLine, tileBorderPx)
	if selected {
		drawSelectedTileOutline(a.cctx, x, y, w, h)
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
		jpeg, err := a.cl.GetTilePreview(context.Background(), tileID)
		a.urlPreview.ClearPending(tileID)
		if err != nil || len(jpeg) == 0 {
			return
		}
		a.urlPreview.Put(tileID, jpeg, func() { a.draw() })
	}()
}
