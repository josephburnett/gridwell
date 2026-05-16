//go:build js && wasm

package main

import (
	"encoding/json"
	"strconv"
	"syscall/js"

	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/internal/urldriver"
)

// streamViewport is the server-side Chromium viewport URL tiles
// render at in v1. The user's pane scales the streamed image to fit;
// coordinates flowing the other way (input events) are mapped from
// pane-local screen coords to this viewport space before being sent.
const (
	streamViewportW = 1280
	streamViewportH = 800
)

// urlStreamConn is the live state for one URLStream WebSocket — one
// per pane that's descended into a live URL tile. The js.Func
// handlers are tracked so we can Release them on close.
//
// pending buffers JSON input messages that arrive between WebSocket
// construction and the open event firing. WebSocket.send throws if
// called in any readyState other than OPEN (1), so we queue while
// CONNECTING and flush on open. This prevents the "wake → immediately
// click" race where the first click would otherwise fire while the
// WS was still handshaking.
type urlStreamConn struct {
	ws     js.Value
	tileID int64
	paneID string

	onMessage js.Func
	onOpen    js.Func
	onClose   js.Func
	onError   js.Func

	pending []string
	closed  bool
}

// openURLStream opens a WebSocket to /rpc/URLStream for the (paneID,
// tileID) pair. Incoming binary frames are pushed into the URL
// preview cache (which the descent renderer draws from). If a stream
// is already open for that pane, the old one is closed first.
func (a *App) openURLStream(paneID string, tileID int64) {
	a.closeURLStream(paneID)

	// ws:// or wss:// matching the current page's protocol.
	loc := js.Global().Get("location")
	proto := "ws:"
	if loc.Get("protocol").String() == "https:" {
		proto = "wss:"
	}
	host := loc.Get("host").String()
	url := proto + "//" + host + "/rpc/URLStream?tile_id=" + strconv.FormatInt(tileID, 10)
	ws := js.Global().Get("WebSocket").New(url)
	ws.Set("binaryType", "arraybuffer")

	conn := &urlStreamConn{ws: ws, tileID: tileID, paneID: paneID}

	conn.onMessage = js.FuncOf(func(_ js.Value, args []js.Value) any {
		data := args[0].Get("data")
		switch t := data.Type(); t {
		case js.TypeString:
			a.handleURLStreamText(tileID, data.String())
		case js.TypeObject:
			// Binary frame: ArrayBuffer → bytes → into preview cache.
			u8 := js.Global().Get("Uint8Array").New(data)
			b := make([]byte, u8.Get("length").Int())
			js.CopyBytesToGo(b, u8)
			a.urlPreview.Put(tileID, b, func() { a.draw() })
		}
		return nil
	})
	conn.onOpen = js.FuncOf(func(_ js.Value, _ []js.Value) any {
		// Flush any input messages queued while we were CONNECTING.
		// Safe to ws.Call now: readyState is OPEN.
		pending := conn.pending
		conn.pending = nil
		for _, p := range pending {
			conn.ws.Call("send", p)
		}
		return nil
	})
	conn.onClose = js.FuncOf(func(_ js.Value, _ []js.Value) any {
		a.releaseURLStream(paneID, conn)
		return nil
	})
	conn.onError = js.FuncOf(func(_ js.Value, _ []js.Value) any {
		// The close handler will fire after error; nothing to do here.
		return nil
	})
	ws.Call("addEventListener", "message", conn.onMessage)
	ws.Call("addEventListener", "open", conn.onOpen)
	ws.Call("addEventListener", "close", conn.onClose)
	ws.Call("addEventListener", "error", conn.onError)

	if a.urlStreams == nil {
		a.urlStreams = map[string]*urlStreamConn{}
	}
	a.urlStreams[paneID] = conn
}

// closeURLStream closes the stream for paneID, if any. Idempotent.
func (a *App) closeURLStream(paneID string) {
	conn, ok := a.urlStreams[paneID]
	if !ok {
		return
	}
	conn.closed = true
	// Closing the underlying WS triggers our onClose, which calls
	// releaseURLStream. Be defensive in case the WS is already gone.
	if conn.ws.Truthy() {
		conn.ws.Call("close")
	}
}

// releaseURLStream cleans up the js.Func handlers and removes the
// conn from the App's stream map. Called from the onClose handler.
func (a *App) releaseURLStream(paneID string, conn *urlStreamConn) {
	if cur, ok := a.urlStreams[paneID]; ok && cur == conn {
		delete(a.urlStreams, paneID)
	}
	conn.onMessage.Release()
	conn.onOpen.Release()
	conn.onClose.Release()
	conn.onError.Release()
}

// handleURLStreamText processes a JSON text message from the server.
// Currently only `nav` (navigation events) are sent. Updates the
// cached tile URL so re-renders show the new address.
func (a *App) handleURLStreamText(tileID int64, payload string) {
	var msg struct {
		Kind string `json:"kind"`
		URL  string `json:"url,omitempty"`
	}
	if err := json.Unmarshal([]byte(payload), &msg); err != nil {
		return
	}
	if msg.Kind != "nav" {
		return
	}
	// Update the cached tile's URLString in any grid that holds it.
	a.updateCachedTileURL(tileID, msg.URL)
}

// updateCachedTileURL walks every cached grid and rewrites the
// URLString field on a tile with the given id. Doesn't trigger
// rerender on its own.
func (a *App) updateCachedTileURL(tileID int64, newURL string) {
	for _, gid := range a.c.KnownGridIDs() {
		g, ok := a.c.Grid(gid)
		if !ok {
			continue
		}
		t, ok := g.Tiles[tileID]
		if !ok {
			continue
		}
		t.URLString = newURL
		a.c.UpdateTile(gid, t)
	}
}

// sendURLStreamInput marshals an InputEvent to JSON and sends it on
// the WebSocket for the given pane. Behavior depends on WebSocket
// readyState:
//
//   CONNECTING (0)  → queue in conn.pending; flushed by onOpen.
//   OPEN       (1)  → send immediately.
//   CLOSING/CLOSED  → drop. The conn will be removed from the map
//                     by onClose; the next interaction will reopen
//                     via syncURLStreamForPane.
//
// Calling ws.send() outside OPEN throws a JS exception; ws.Call
// propagates that as a Go panic, which would crash the calling
// goroutine. Gating on readyState is the canonical fix.
func (a *App) sendURLStreamInput(paneID string, ev urldriver.InputEvent) {
	conn, ok := a.urlStreams[paneID]
	if !ok || conn.closed || !conn.ws.Truthy() {
		return
	}
	payload, err := json.Marshal(struct {
		Kind      string  `json:"kind"`
		X         float64 `json:"x,omitempty"`
		Y         float64 `json:"y,omitempty"`
		Button    string  `json:"button,omitempty"`
		DeltaY    float64 `json:"delta_y,omitempty"`
		Key       string  `json:"key,omitempty"`
		Code      string  `json:"code,omitempty"`
		Modifiers int64   `json:"modifiers,omitempty"`
		Width     int64   `json:"width,omitempty"`
		Height    int64   `json:"height,omitempty"`
	}{
		Kind:      string(ev.Kind),
		X:         ev.X,
		Y:         ev.Y,
		Button:    ev.Button,
		DeltaY:    ev.DeltaY,
		Key:       ev.Key,
		Code:      ev.Code,
		Modifiers: ev.Modifiers,
		Width:     ev.Width,
		Height:    ev.Height,
	})
	if err != nil {
		return
	}
	switch conn.ws.Get("readyState").Int() {
	case 0: // CONNECTING — queue, flushed by onOpen
		conn.pending = append(conn.pending, string(payload))
	case 1: // OPEN
		conn.ws.Call("send", string(payload))
	default: // 2 CLOSING, 3 CLOSED
		// drop — onClose will fire and we'll reopen later if needed
	}
}

// syncURLStreamForPane brings the URLStream WS state for pane p into
// agreement with the pane's current focus and the focused tile's
// liveness:
//
//   - not descended into a URL tile  → no WS (close if any)
//   - descended into dormant URL tile → no WS
//   - descended into live URL tile    → WS open for that tile
//
// Called reactively from the SSE handler (so a Live→true transition
// after descent immediately opens the stream) and as a safety net
// from each input handler (so clicks land even if the SSE pass
// missed them).
func (a *App) syncURLStreamForPane(p *pane.Pane) {
	if p == nil {
		return
	}
	if !a.isURLDescent(p) {
		a.closeURLStream(p.ID)
		return
	}
	gid := a.gridIDForPath(p.Path)
	g, ok := a.c.Grid(gid)
	if !ok {
		return
	}
	t, ok := g.Tiles[p.FileFocus]
	if !ok {
		return
	}
	existing, hasConn := a.urlStreams[p.ID]
	if t.Live {
		if !hasConn || existing.tileID != t.ID {
			a.openURLStream(p.ID, t.ID)
		}
		return
	}
	if hasConn {
		a.closeURLStream(p.ID)
	}
}

// hasURLStreamForTile reports whether any open URLStream connection
// is currently subscribed to the given tile. Used to skip the
// url_preview_updated-driven refetch when a WS is already in flight
// for the same tile (the WS delivers the bytes directly).
func (a *App) hasURLStreamForTile(tileID int64) bool {
	for _, conn := range a.urlStreams {
		if conn != nil && conn.tileID == tileID {
			return true
		}
	}
	return false
}

// isURLDescent reports whether pane p is currently descended into a
// URL tile. Used by the input handlers to switch from gridwell-native
// gestures to URLStream input forwarding.
func (a *App) isURLDescent(p *pane.Pane) bool {
	if p == nil || p.FileFocus == 0 {
		return false
	}
	gid := a.gridIDForPath(p.Path)
	g, ok := a.c.Grid(gid)
	if !ok {
		return false
	}
	t, ok := g.Tiles[p.FileFocus]
	if !ok {
		return false
	}
	return t.IsURL()
}

// paneToStreamCoords maps a screen coordinate (sx, sy) inside a
// pane's content box to the corresponding (X, Y) in server-side
// viewport space. The server's viewport is fixed at streamViewportW
// × streamViewportH in v1. We translate from the content box (NOT
// the full pane rect): the page renders inside paneContentBox, so
// the user's click at screen (sx, sy) corresponds to the same
// location in viewport space only after subtracting the margin and
// scaling against the content box size.
func paneToStreamCoords(r paneRect, sx, sy float64) (float64, float64) {
	cx, cy, cw, ch := paneContentBox(r)
	if cw <= 0 || ch <= 0 {
		return 0, 0
	}
	x := (sx - cx) * streamViewportW / cw
	y := (sy - cy) * streamViewportH / ch
	return x, y
}
