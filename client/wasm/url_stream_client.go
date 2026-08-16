//go:build js && wasm

package main

import (
	"context"
	"fmt"
	"slices"
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/panebox"
	"github.com/josephburnett/gridwell/client/wsbar"
)

// urlView is the renderer-side handle for one live URL tile — a native
// WebContentsView hosted by the Electron main process and floated over the
// pane's content box. It replaced the old urlStreamConn (a /rpc/URLStream
// WebSocket streaming rod JPEGs). The live handle lives on paneLocal.urlView,
// reached through a.urlViewFor(paneID) (nil = no live URL descent).
type urlView struct {
	tileID   string
	objectID string
	paneID   string
	bounds   viewBounds
	// anchor + path are the plugin-root grid id and the descent path to the
	// grid that holds this URL tile, captured when the view went live. The
	// freeze (SetURLState) needs them to resolve this tile's leaf grid
	// (copy-on-clone: tiles are unshared, so the write is in-place — no fork).
	anchor string
	path   []string
	// version is the target tile's version at place time — the freeze
	// fallback when the tile isn't in any cached grid (a url LINK's target
	// lives in a foreign grid the client may never have loaded). 0 = rely
	// on the cache lookup (the ordinary same-plugin case).
	version int64
	// page marks a serves_page view (2026-08-11): the address is the
	// derived /content/ door URL, and the close skips the SetURLState
	// freeze writeback — the owning plugin (fs) derives its frozen face
	// from the content itself (GetTilePreview) and stores nothing.
	page bool
	// durable mirrors placeURLView's freeze eligibility: false for a page
	// view or an ephemeral visit. The unload beacon reads it — an
	// ephemeral tile's state must never be persisted by a tab close.
	durable bool
	// navDirty marks that the live page navigated since place — the tile
	// row's url on the server is stale. Read by the unload beacon
	// (SetURLStateBeacon): navigation state used to persist exactly once,
	// at a teardown whose IPC reply never arrives during unload (audit
	// #2, 2026-08-14), so closing the tab lost the trail every time.
	navDirty bool
	// lastTitle is the most recent page title from the nav events, for
	// the unload beacon (the freeze path gets its title from the bridge
	// reply, which the unload path cannot wait for).
	lastTitle string
}

// urlLog writes a tagged debug message to the browser console.
func urlLog(format string, args ...any) {
	msg := "[urlview] " + fmt.Sprintf(format, args...)
	js.Global().Get("console").Call("log", msg)
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

// urlTileForPane resolves the web-content tile a pane is descended into —
// a url tile or a serves_page tile; both go live through the same view.
func (a *App) urlTileForPane(p *pane.Pane, tileID string) (rpc.Tile, bool) {
	if g, ok := a.c.Grid(a.gridIDForPane(p)); ok {
		if t, ok := g.Tiles[tileID]; ok && t.WebContent() {
			return t, true
		}
	}
	// Off-grid (ephemeral) tile — focused in the scratch grid without
	// re-anchoring the pane onto it: resolve by id from any cached grid.
	if t := a.findTileByID(tileID); t != nil && t.WebContent() {
		return *t, true
	}
	return rpc.Tile{}, false
}

// webAddress resolves the address a web-content tile presents at: a url
// tile's own frozen URLString, or the /content/ door address for a
// serves_page tile — derived here at use time (rpc.PageURL), never
// persisted, because the desktop origin is an ephemeral port.
func (a *App) webAddress(t *rpc.Tile) string {
	if t.Kind == rpc.KindURL {
		return t.URLString
	}
	if t.ServesPage {
		// ContentID: the one client-side link resolution — the door would
		// re-resolve server-side, but every content op keys by the owner.
		return rpc.PageURL(a.origin, a.contentToken, t.ContentID())
	}
	return ""
}

// tileVersionAt returns the cached version of the tile at (anchor, path,
// tileID), or 0 if it isn't cached. Read at freeze time so the url/shell
// preview writebacks can claim the right version for their in-place,
// versioned edits.
func (a *App) tileVersionAt(anchor string, path []string, tileID string) int64 {
	g, ok := a.c.Grid(a.gridIDForPathFrom(anchor, path))
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
// the tile's persistent session partition. Bounds are computed from the
// pane's current rect and kept in step by syncURLViews. No-op outside the
// Electron shell.
func (a *App) openURLStream(p *pane.Pane, tileID string) {
	if !a.caps.LiveURL {
		urlLog("live URL unavailable on this host (no Electron bridge); tile stays frozen")
		return
	}
	t, ok := a.urlTileForPane(p, tileID)
	if !ok {
		return
	}
	if t.URLFrozen {
		// Going live IS the unfreeze (issue #237): the reconnect gesture
		// clears the standing intent, so the two facts never coexist.
		// (Auto-live never reaches here while the intent is set —
		// DecideAutoLive blocks it — so this only fires on the explicit
		// reconnect click.)
		tid, version := t.ID, t.Version
		go func() {
			cleared, err := a.cl.SetURLFrozen(context.Background(), &rpc.SetURLFrozenRequest{
				TileID: tid, Version: version, Frozen: false,
			})
			if err != nil {
				a.surfaceRPCError("SetTile", err)
				return
			}
			a.c.UpdateTile(cleared.GridID, *cleared)
		}()
	}
	if t.LinkTargetID == "" {
		a.placeURLView(p.ID, t, 0)
		return
	}
	// A url LINK goes live as its TARGET: the url string, session partition
	// (the target's plugin owns the cookies — the thing is the target's),
	// history, and the freeze writeback all belong to the tile that owns the
	// content. The target row lives in a foreign grid the client has likely
	// never loaded, so fetch it; the view places when the row arrives.
	paneID := p.ID
	go func() {
		target, err := a.cl.GetTile(context.Background(), t.LinkTargetID)
		if err != nil {
			a.surfaceRPCError("GetTile", err)
			return
		}
		if a.tree.FindPane(paneID) == nil {
			return // pane closed while the target row was in flight
		}
		a.placeURLView(paneID, *target, target.Version)
	}()
}

// placeURLView places the native WebContentsView for pane paneID showing
// tile t (always the CONTENT-owning row — a link never reaches here).
// version is the freeze fallback for a foreign target (see urlView.version).
func (a *App) placeURLView(paneID string, t rpc.Tile, version int64) {
	p := a.tree.FindPane(paneID)
	if p == nil {
		return
	}
	// Idempotent: the pane already shows this content live (a keep-alive
	// return, issue #249 — nothing froze, so there is nothing to redo).
	if v := a.urlViewFor(paneID); v != nil && v.tileID == t.ID {
		return
	}
	// ONE live surface per content tile (issue #249, generalizing the
	// same-level rule): any OTHER pane — at any stack level — holding a
	// live view on this content freezes now; the opener takes over.
	for otherID, pl := range a.locals {
		if otherID != paneID && pl.urlView != nil && pl.urlView.tileID == t.ID {
			a.closeURLStream(otherID, true)
		}
	}
	r := a.barAwarePaneRect(p)
	b := contentViewBounds(r)
	page := t.Kind != rpc.KindURL && t.ServesPage
	a.local(p.ID).urlView = &urlView{tileID: t.ID, objectID: t.ObjectID, paneID: p.ID, bounds: b, anchor: p.Anchor, path: slices.Clone(p.Path), version: version, page: page}
	// durable = the DESCENDED row survives ascent: false for an ephemeral
	// visit, which gets no Freeze Page in the context menu (issue #240).
	// A page view is never durable in this sense either: it carries no
	// standing freeze and no history writeback — its frozen face is the
	// plugin's own derivation.
	durable := !page
	if tile, ok := a.descendedTile(p); ok && a.isEphemeralTile(p, &tile) {
		durable = false
	}
	a.local(p.ID).urlView.durable = durable
	addr := a.webAddress(&t)
	urlLog("place pane=%s tile=%s obj=%s url=%s", p.ID, t.ID, t.ObjectID, addr)
	bridgePlace(p.ID, t.ID, t.ObjectID, addr, b, contentZoomOf(&t), t.URLHistory, durable)
	a.draw()
}

// closeURLStream tears down the live view for paneID: it removes the
// WebContentsView, captures a final frame, and (when freeze is true) persists
// the frozen preview + address + title via SetURLState. An ephemeral tile's
// ascent passes freeze=false — the tile is about to be deleted, and a freeze
// would bump its version out from under the delete (issue #85). Idempotent.
func (a *App) closeURLStream(paneID string, freeze bool) {
	pl, ok := a.localIf(paneID)
	if !ok || pl.urlView == nil {
		return
	}
	v := pl.urlView
	pl.urlView = nil
	tileID := v.tileID
	// The freeze-frame cache is read by ContentID (a link and its target
	// share one face); the put must use the same key or a link's final
	// frame lands where nobody reads.
	previewKey := tileID
	if ct := a.cachedTileByID(tileID); ct != nil {
		previewKey = ct.ContentID()
	}
	anchor := v.anchor
	path := slices.Clone(v.path)
	urlLog("close pane=%s tile=%s", paneID, tileID)
	bridgeRemove(paneID, func(jpeg []byte, url, title, history string) {
		// A page view persists NOTHING on close: the plugin owns the frozen
		// face (fs derives a thumbnail from the file), and its store has no
		// url state to write. The wildcard put below still shows the final
		// frame for the rest of the session.
		if freeze && !v.page && (len(jpeg) > 0 || url != "" || title != "") {
			// Look up the tile's current version from cache so the freeze is
			// a versioned, in-place content edit (copy-on-clone: nothing is
			// shared, so there is no fork — the write lands on this tile's row).
			// A foreign target (live through a url link) isn't in any cached
			// grid; fall back to the version captured at place time.
			version := a.tileVersionAt(anchor, path, tileID)
			if version == 0 {
				version = v.version
			}
			// doFreezeWrite owns the leaving-gesture rule: a version conflict
			// (a foreign writer or auto title capture racing the close)
			// re-claims once and retries; a remaining failure surfaces AND
			// resyncs the grid — the freeze the user just saw is not
			// persisted and the preview will revert on next load (charter
			// §6; issue #156 — this path used to bypass the dispatcher).
			gid := a.gridIDForPathFrom(anchor, path)
			go a.doFreezeWrite("SetURLState", gid, tileID, version,
				"urlfreeze", "page preview save failed",
				func(version int64) error {
					_, err := a.cl.SetURLState(context.Background(), &rpc.SetURLStateRequest{
						TileID:  tileID,
						Version: version,
						JPEG:    jpeg, URL: url, Title: title, History: history,
					})
					if err != nil {
						urlLog("SetURLState tile=%s err=%v", tileID, err)
					}
					return err
				})
		}
		if len(jpeg) > 0 {
			// Reflect the just-frozen frame immediately so the pane (and
			// any mirror) shows the final state without waiting for the
			// SetURLState round-trip + preview re-fetch.
			a.urlPreview.PutWildcard(previewKey, jpeg, func() { a.draw() })
		}
		a.draw()
	})
}

// freezeURLPaneByIntent runs the explicit freeze gesture (issue #237, the
// context menu's "Freeze Page"): persist the user's STANDING freeze, then
// tear the live view down through the ordinary freeze writeback. The
// intent lands on the DESCENDED row (p.TextFocus) — for a url link that
// is the link row itself: the freeze is this reference's presentation,
// not the content owner's, and it is the row the next descent's
// DecideAutoLive reads. The intent write goes first: it is framing
// (version untouched), while the teardown's SetURLState bumps the
// version. Ephemeral visits are skipped — they die on ascent and carry
// no durable intent.
func (a *App) freezeURLPaneByIntent(paneID string) {
	p := a.tree.FindPane(paneID)
	pl, ok := a.localIf(paneID)
	if p == nil || !ok || pl.urlView == nil || p.TextFocus == "" {
		return
	}
	tile, ok := a.descendedTile(p)
	if !ok || tile.Kind != rpc.KindURL || a.isEphemeralTile(p, &tile) {
		return
	}
	tid, version := tile.ID, tile.Version
	go func() {
		t, err := a.cl.SetURLFrozen(context.Background(), &rpc.SetURLFrozenRequest{
			TileID: tid, Version: version, Frozen: true,
		})
		if err != nil {
			// The freeze the user asked for did not stick — surface it
			// (charter §6); the teardown below still parks the view.
			a.surfaceRPCError("SetTile", err)
		} else {
			a.c.UpdateTile(t.GridID, *t)
		}
		a.closeURLStream(paneID, true)
		a.draw()
	}()
}

// closeAllURLStreams tears down every live view. Used on beforeunload so the
// freeze writes fire before the page goes away.
func (a *App) closeAllURLStreams() {
	ids := make([]string, 0, len(a.locals))
	for id, pl := range a.locals {
		if pl.urlView != nil {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		a.closeURLStream(id, true)
	}
}

// syncURLViews tracks every live view to its pane's content box each frame
// (so pan/zoom/resize/pane-edits move the native view with the canvas) and
// parks views off-screen during gestures that need to paint canvas overlays
// on top (drag ghost, palette, right-drag pane preview). Called at the end
// of draw(), mirroring syncShellOverlayPosition.
func (a *App) syncURLViews() {
	rects := a.layoutPanes()
	hidden := a.liveOverlaysHidden()
	for paneID, pl := range a.locals {
		v := pl.urlView
		if v == nil {
			continue
		}
		r, ok := rects[paneID]
		if !ok {
			bridgeSetHidden(paneID, true, false)
			continue
		}
		// The focused pane's band is bar territory (issue #220): the live
		// view's content box carves it out so it can never occlude the bar.
		b := contentViewBounds(panebox.BarInset(r, paneID == a.tree.Focus, wsbar.RowH))
		v.bounds = b
		bridgeSetBounds(paneID, b)
		// The corner control belongs to the focused pane only — same rule the
		// canvas applies to every other per-pane control (render.go drawPane).
		bridgeSetHidden(paneID, hidden, paneID == a.tree.Focus)
	}
}

// liveOverlaysHidden reports whether live overlays (native URL views and the
// xterm shell host divs) should be parked this frame. Both kinds paint above
// the canvas and swallow mouse input over their rect, so any gesture that
// previews on the canvas — or drags a boundary across an overlay — must hide
// them first, else the overlay eats the move/up events and the gesture stalls.
func (a *App) liveOverlaysHidden() bool {
	// The url modal is DOM — a live WebContentsView would paint OVER it,
	// hiding what you type (issue #131) — so it parks the views too. The
	// rename input does NOT park anymore: it opens in the bottom bar (issue
	// #213), outside every live view's rect.
	return a.dragging != nil || a.rightDrag != nil || a.leftResize != nil || a.menu.IsOpen() || a.urlModalOpen || a.instPickerOpen
}

// isURLDescent reports whether pane p is currently descended into WEB
// CONTENT — a url tile or a serves_page tile (2026-08-11; the two present
// identically). Drives the input handlers' branch between gridwell-native
// gestures and (now-native) URL interaction, and the bar slot's url-family
// affordances.
func (a *App) isURLDescent(p *pane.Pane) bool {
	if p == nil {
		return false
	}
	// descendedTile resolves an ephemeral url visit too (focused off the pane's
	// grid, in the scratch grid), so live-url input handling works for it.
	t, ok := a.descendedTile(p)
	if !ok {
		return false
	}
	return t.WebContent()
}

// updateCachedTileURL walks every cached grid and rewrites the URLString
// field on a tile with the given id. Driven by nav events from the bridge.
// URL tiles only: a page view's navigations are within plugin-served
// content — the tile row has no url_string fact to shadow.
func (a *App) updateCachedTileURL(tileID string, newURL string) {
	for _, gid := range a.c.KnownGridIDs() {
		g, ok := a.c.Grid(gid)
		if !ok {
			continue
		}
		t, ok := g.Tiles[tileID]
		if !ok || t.Kind != rpc.KindURL {
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
