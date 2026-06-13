//go:build js && wasm

package main

import (
	"context"
	"fmt"
	"slices"
	"syscall/js"

	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/panebox"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// urlView is the renderer-side handle for one live URL tile — a native
// WebContentsView hosted by the Electron main process and floated over the
// pane's content box. It replaced the old urlStreamConn (a /rpc/URLStream
// WebSocket streaming rod JPEGs); the map field is still named urlStreams so
// the many liveness checks (a.urlStreams[p.ID] != nil) read unchanged.
type urlView struct {
	tileID   int64
	objectID string
	paneID   string
	bounds   viewBounds
	// path is the descent path to the grid that holds this URL tile,
	// captured when the view went live. The freeze (SetURLState) needs it
	// to fork a shared grid so the write lands in this clone's row only.
	path []int64
}

// urlLog writes a tagged debug message to the browser console.
func urlLog(format string, args ...any) {
	msg := "[urlview] " + fmt.Sprintf(format, args...)
	js.Global().Get("console").Call("log", msg)
}

// paneStreamSize is a thin wasm adapter over the pure panebox helper,
// binding the renderer's paneBorderPx. Retained for the refresh-gesture
// callsites that pass a content size into openURLStream.
func paneStreamSize(r pane.Rect) (int64, int64) {
	return panebox.StreamViewportSize(r, paneBorderPx)
}

// contentViewBounds maps a pane's screen rect to the content-box rectangle a
// hosted webview should occupy — the pane minus its border band, in CSS px.
// The view fills the whole content box; the corner controls (ascend / back)
// float on top as a small native overlay view (see webviews.ts), since a
// canvas-drawn button can't paint above a native WebContentsView.
func contentViewBounds(r pane.Rect) viewBounds {
	b := panebox.ContentBox(r, paneBorderPx)
	return viewBounds{X: b.X, Y: b.Y, W: b.W, H: b.H}
}

// urlTileForPane resolves the URL tile a pane is descended into.
func (a *App) urlTileForPane(p *pane.Pane, tileID int64) (rpc.Tile, bool) {
	gid := a.gridIDForPath(p.Path)
	g, ok := a.c.Grid(gid)
	if !ok {
		return rpc.Tile{}, false
	}
	t, ok := g.Tiles[tileID]
	if !ok || t.Kind != rpc.KindURL {
		return rpc.Tile{}, false
	}
	return t, true
}

// urlTileVersion returns the cached version of the URL tile at (path,
// tileID), or 0 if it isn't cached. Read at freeze time so SetURLState can
// claim the right version for its COW fork.
func (a *App) urlTileVersion(path []int64, tileID int64) int64 {
	g, ok := a.c.Grid(a.gridIDForPath(path))
	if !ok {
		return 0
	}
	if t, ok := g.Tiles[tileID]; ok {
		return t.Version
	}
	return 0
}

// openURLStream goes live: it asks the Electron main process to place a
// native WebContentsView for (pane, tile) over the pane's content box, on
// the tile's persistent session partition. The w/h args are vestigial (the
// old WebSocket viewport); bounds are computed from the pane's current rect
// and kept in step by syncURLViews. No-op outside the Electron shell.
func (a *App) openURLStream(p *pane.Pane, tileID int64, _, _ int64) {
	if !bridgeAvailable() {
		urlLog("no bridge; live URL unavailable (not running in Electron shell)")
		return
	}
	t, ok := a.urlTileForPane(p, tileID)
	if !ok {
		return
	}
	r := a.paneRectByID(p.ID)
	b := contentViewBounds(r)
	if a.urlStreams == nil {
		a.urlStreams = map[string]*urlView{}
	}
	a.urlStreams[p.ID] = &urlView{tileID: tileID, objectID: t.ObjectID, paneID: p.ID, bounds: b, path: slices.Clone(p.Path)}
	urlLog("place pane=%s tile=%d obj=%s url=%s", p.ID, tileID, t.ObjectID, t.URLString)
	bridgePlace(p.ID, tileID, t.ObjectID, t.URLString, b)
	a.draw()
}

// closeURLStream freezes and tears down the live view for paneID: it removes
// the WebContentsView, captures a final frame, and persists the frozen
// preview + address + title via SetURLState. Idempotent.
func (a *App) closeURLStream(paneID string) {
	v, ok := a.urlStreams[paneID]
	if !ok {
		return
	}
	delete(a.urlStreams, paneID)
	tileID := v.tileID
	path := slices.Clone(v.path)
	urlLog("close pane=%s tile=%d", paneID, tileID)
	bridgeRemove(paneID, func(jpeg []byte, url, title string) {
		if len(jpeg) > 0 || url != "" || title != "" {
			// Look up the tile's current version from cache so the freeze is
			// a versioned content edit that forks a shared (cloned) grid.
			version := a.urlTileVersion(path, tileID)
			go func() {
				_, err := a.cl.SetURLState(context.Background(), &rpc.SetURLStateRequest{
					Path:    rpc.Path{WellIDs: path},
					TileID:  tileID,
					Version: version,
					JPEG:    jpeg, URL: url, Title: title,
				})
				if err != nil {
					urlLog("SetURLState tile=%d err=%v", tileID, err)
				}
			}()
		}
		if len(jpeg) > 0 {
			// Reflect the just-frozen frame immediately so the pane (and
			// any mirror) shows the final state without waiting for the
			// SetURLState round-trip + preview re-fetch.
			a.urlPreview.PutWildcard(tileID, jpeg, func() { a.draw() })
		}
		a.draw()
	})
}

// closeAllURLStreams tears down every live view. Used on beforeunload so the
// freeze writes fire before the page goes away.
func (a *App) closeAllURLStreams() {
	ids := make([]string, 0, len(a.urlStreams))
	for id := range a.urlStreams {
		ids = append(ids, id)
	}
	for _, id := range ids {
		a.closeURLStream(id)
	}
}

// syncURLViews tracks every live view to its pane's content box each frame
// (so pan/zoom/resize/pane-edits move the native view with the canvas) and
// parks views off-screen during gestures that need to paint canvas overlays
// on top (drag ghost, palette, right-drag pane preview). Called at the end
// of draw(), mirroring syncShellOverlayPosition.
func (a *App) syncURLViews() {
	if len(a.urlStreams) == 0 {
		return
	}
	rects := a.layoutPanes()
	hidden := a.liveOverlaysHidden()
	for paneID, v := range a.urlStreams {
		r, ok := rects[paneID]
		if !ok {
			bridgeSetHidden(paneID, true)
			continue
		}
		b := contentViewBounds(r)
		v.bounds = b
		bridgeSetBounds(paneID, b)
		bridgeSetHidden(paneID, hidden)
	}
}

// liveOverlaysHidden reports whether live overlays (native URL views and the
// xterm shell host divs) should be parked this frame. Both kinds paint above
// the canvas and swallow mouse input over their rect, so any gesture that
// previews on the canvas — or drags a boundary across an overlay — must hide
// them first, else the overlay eats the move/up events and the gesture stalls.
func (a *App) liveOverlaysHidden() bool {
	return a.dragging != nil || a.rightDrag != nil || a.leftResize != nil || a.menuOpen
}

// isURLDescent reports whether pane p is currently descended into a URL
// tile. Drives the input handlers' branch between gridwell-native gestures
// and (now-native) URL interaction.
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

// updateCachedTileURL walks every cached grid and rewrites the URLString
// field on a tile with the given id. Driven by nav events from the bridge.
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

// paneRectByID looks up the screen rect for the given pane via a fresh
// layout pass.
func (a *App) paneRectByID(paneID string) pane.Rect {
	rs := a.layoutPanes()
	return rs[paneID]
}
