//go:build js && wasm

package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"syscall/js"

	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/panebox"
	"github.com/josephburnett/gridwell/internal/rpc"
	"github.com/josephburnett/gridwell/internal/urldriver"
)

// urlStreamConn is one URLStream WebSocket — one per pane descended
// into a live URL tile. The js.Func handlers are tracked so we can
// Release them on close. pending buffers JSON messages that arrive
// between WebSocket construction and the open event firing (which
// WebSocket.send rejects with an exception).
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

	// lastSentW/H is the most recent viewport we informed the server
	// of, used to suppress duplicate "viewport" messages every draw.
	lastSentW, lastSentH int64
}

// urlLog writes a tagged debug message to the browser console.
// Keeps URLStream-related logs grepable in devtools.
func urlLog(format string, args ...any) {
	msg := "[urlstream] " + fmt.Sprintf(format, args...)
	js.Global().Get("console").Call("log", msg)
}

// paneStreamSize / paneStreamLocal are thin wasm adapters over the
// pure panebox helpers, binding the wasm renderer's paneBorderPx.
func paneStreamSize(r pane.Rect) (int64, int64) {
	return panebox.StreamViewportSize(r, paneBorderPx)
}

func paneStreamLocal(r pane.Rect, sx, sy float64) (float64, float64) {
	return panebox.StreamLocalCoords(r, paneBorderPx, sx, sy)
}

// openURLStream opens a WebSocket to /rpc/URLStream for the (pane,
// tileID) pair, sized w×h. The viewport is sent as the first message
// on open. If a stream is already open for that pane, the old one is
// closed first.
func (a *App) openURLStream(p *pane.Pane, tileID int64, w, h int64) {
	a.closeURLStream(p.ID)

	loc := js.Global().Get("location")
	proto := "ws:"
	if loc.Get("protocol").String() == "https:" {
		proto = "wss:"
	}
	host := loc.Get("host").String()
	url := proto + "//" + host + "/rpc/URLStream?tile_id=" + strconv.FormatInt(tileID, 10)
	urlLog("open pane=%s tile=%d url=%s w=%d h=%d", p.ID, tileID, url, w, h)
	ws := js.Global().Get("WebSocket").New(url)
	ws.Set("binaryType", "arraybuffer")

	conn := &urlStreamConn{ws: ws, tileID: tileID, paneID: p.ID}
	// Queue the initial viewport so it's the first thing the server
	// sees once the handshake completes.
	conn.pending = append(conn.pending, viewportPayload(w, h))
	conn.lastSentW, conn.lastSentH = w, h

	conn.onMessage = js.FuncOf(func(_ js.Value, args []js.Value) any {
		data := args[0].Get("data")
		switch data.Type() {
		case js.TypeString:
			a.handleURLStreamText(tileID, data.String())
		case js.TypeObject:
			u8 := js.Global().Get("Uint8Array").New(data)
			b := make([]byte, u8.Get("length").Int())
			js.CopyBytesToGo(b, u8)
			a.urlPreview.Put(tileID, b, func() { a.draw() })
		}
		return nil
	})
	conn.onOpen = js.FuncOf(func(_ js.Value, _ []js.Value) any {
		pending := conn.pending
		conn.pending = nil
		urlLog("onOpen pane=%s tile=%d flushing=%d", p.ID, tileID, len(pending))
		for _, q := range pending {
			conn.ws.Call("send", q)
		}
		return nil
	})
	conn.onClose = js.FuncOf(func(_ js.Value, args []js.Value) any {
		code := -1
		reason := ""
		clean := false
		if len(args) > 0 {
			code = args[0].Get("code").Int()
			reason = args[0].Get("reason").String()
			clean = args[0].Get("wasClean").Bool()
		}
		urlLog("onClose pane=%s tile=%d code=%d clean=%v reason=%q", p.ID, tileID, code, clean, reason)
		_ = code
		_ = reason
		_ = clean
		a.releaseURLStream(p.ID, conn)
		// On any close (user-initiated, takeover, or unexpected crash)
		// the pane reverts to showing the frozen JPEG preview. We do not
		// distinguish close reasons: a takeover close looks identical to
		// an unexpected close from the client side (same WS close frame),
		// and the urlStreamLost overlay has been removed in favour of the
		// Phase-C border-tint cue. The user can refresh again if needed.
		a.draw()
		return nil
	})
	conn.onError = js.FuncOf(func(_ js.Value, _ []js.Value) any {
		urlLog("onError pane=%s tile=%d readyState=%d", p.ID, tileID, conn.ws.Get("readyState").Int())
		return nil
	})
	ws.Call("addEventListener", "message", conn.onMessage)
	ws.Call("addEventListener", "open", conn.onOpen)
	ws.Call("addEventListener", "close", conn.onClose)
	ws.Call("addEventListener", "error", conn.onError)

	if a.urlStreams == nil {
		a.urlStreams = map[string]*urlStreamConn{}
	}
	a.urlStreams[p.ID] = conn
}

// closeURLStream closes the stream for paneID, if any. Idempotent.
func (a *App) closeURLStream(paneID string) {
	conn, ok := a.urlStreams[paneID]
	if !ok {
		return
	}
	conn.closed = true
	if conn.ws.Truthy() {
		conn.ws.Call("close")
	}
}

// closeAllURLStreams closes every open URL stream. Used on
// beforeunload so the server's save-and-destroy runs before the tab
// goes away.
func (a *App) closeAllURLStreams() {
	// Snapshot the keys first: closeURLStream triggers onClose →
	// releaseURLStream → delete(a.urlStreams, id), so mutating the map
	// while ranging over it is undefined.
	ids := make([]string, 0, len(a.urlStreams))
	for id := range a.urlStreams {
		ids = append(ids, id)
	}
	for _, id := range ids {
		a.closeURLStream(id)
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

// notifyURLStreamSize informs the server of a new pane content size
// for a descended URL tile. No-op if the size hasn't changed since
// the last notify (so it's safe to call every draw).
func (a *App) notifyURLStreamSize(paneID string, w, h int64) {
	conn, ok := a.urlStreams[paneID]
	if !ok || conn.closed {
		return
	}
	if conn.lastSentW == w && conn.lastSentH == h {
		return
	}
	conn.lastSentW, conn.lastSentH = w, h
	payload := viewportPayload(w, h)
	state := conn.ws.Get("readyState").Int()
	switch state {
	case 0: // CONNECTING — queue, flushed by onOpen
		conn.pending = append(conn.pending, payload)
	case 1: // OPEN
		conn.ws.Call("send", payload)
	}
}

// viewportPayload returns a JSON {"kind":"viewport","width":w,"height":h}
// message ready to send on the WS. Uses the shared rpc.URLStreamMessage
// type so client and server agree on the wire shape.
func viewportPayload(w, h int64) string {
	b, _ := json.Marshal(rpc.URLStreamMessage{
		Kind:   "viewport",
		Width:  w,
		Height: h,
	})
	return string(b)
}

// handleURLStreamText processes a JSON text message from the server.
// Currently only `nav` (navigation events) is sent. Updates the cached
// tile URL so re-renders show the new address.
func (a *App) handleURLStreamText(tileID int64, payload string) {
	var msg rpc.URLStreamServerMessage
	if err := json.Unmarshal([]byte(payload), &msg); err != nil {
		return
	}
	if msg.Kind != "nav" {
		return
	}
	a.updateCachedTileURL(tileID, msg.URL)
}

// updateCachedTileURL walks every cached grid and rewrites the
// URLString field on a tile with the given id.
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
// the WebSocket for the given pane. Behavior depends on readyState:
//
//	CONNECTING (0) → queue in conn.pending; flushed by onOpen.
//	OPEN       (1) → send immediately.
//	CLOSING/CLOSED → drop. The next interaction reopens via
//	                 syncURLStreamForPane.
//
// Calling ws.send() outside OPEN throws a JS exception; ws.Call
// propagates that as a Go panic. The readyState switch is the fix.
func (a *App) sendURLStreamInput(paneID string, ev urldriver.InputEvent) {
	conn, ok := a.urlStreams[paneID]
	if !ok || conn.closed || !conn.ws.Truthy() {
		return
	}
	payload, err := json.Marshal(rpc.URLStreamMessage{
		Kind:      string(ev.Kind),
		X:         ev.X,
		Y:         ev.Y,
		Button:    ev.Button,
		DeltaY:    ev.DeltaY,
		Key:       ev.Key,
		Code:      ev.Code,
		Modifiers: ev.Modifiers,
	})
	if err != nil {
		return
	}
	state := conn.ws.Get("readyState").Int()
	switch state {
	case 0:
		conn.pending = append(conn.pending, string(payload))
		urlLog("queue pane=%s kind=%s queued=%d", paneID, ev.Kind, len(conn.pending))
	case 1:
		conn.ws.Call("send", string(payload))
	default:
		urlLog("drop pane=%s kind=%s state=%d", paneID, ev.Kind, state)
	}
}

// isURLDescent reports whether pane p is currently descended into a
// URL tile. Used by the input handlers to switch from gridwell-native
// gestures to URLStream input forwarding.
func (a *App) isURLDescent(p *pane.Pane) bool {
	if p == nil || p.TextFocus == 0 {
		return false
	}
	gid := a.gridIDForPath(p.Path)
	g, ok := a.c.Grid(gid)
	if !ok {
		return false
	}
	t, ok := g.Tiles[p.TextFocus]
	if !ok {
		return false
	}
	return t.Kind == rpc.KindURL
}

// paneRectByID looks up the screen rect for the given pane via a fresh
// layout pass. Used by code paths (SSE handler, viewport-resize tick)
// that don't have the rect on hand.
func (a *App) paneRectByID(paneID string) pane.Rect {
	rs := a.layoutPanes()
	return rs[paneID]
}
