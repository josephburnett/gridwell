//go:build js && wasm

package main

import (
	"context"
	"slices"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/cache"
	"github.com/josephburnett/gridwell/client/pane"
)

// urlView is the renderer-side handle for one live URL tile: a native
// WebContentsView hosted by the Electron main process and floated over the
// pane's content box. The handle lives on paneLocal.urlView, reached through
// a.urlViewFor(paneID); nil means no live URL descent.
type urlView struct {
	tileID string
	paneID string
	// descentID is the pane frame this view was opened for — the row the pane
	// is descended into, which for a url link is the link row and not tileID,
	// the content owner the view actually shows. The per-frame sweep compares
	// it against the pane's current descent (pane.SurfaceOf) to tell a view
	// that still belongs on screen from one whose pane has moved on.
	descentID string
	bounds    viewBounds
	// anchor and path are the plugin-root grid id and the descent path to
	// the grid that holds this URL tile, captured when the view went live.
	// The freeze (SetURLState) needs them to resolve this tile's leaf grid.
	// Tiles are unshared, so the write is in place.
	anchor string
	path   []string
	// page marks a serves_page view: the address is the derived /content/
	// door URL, and the close skips the SetURLState freeze writeback,
	// because the owning plugin derives its frozen face from the content
	// itself (GetTilePreview) and stores nothing.
	page bool
	// durable mirrors placeURLView's freeze eligibility: false for a page
	// view or an ephemeral visit. The unload beacon reads it — an
	// ephemeral tile's state must never be persisted by a tab close.
	durable bool
	// navDirty marks that the live page navigated since place, so the tile
	// row's url on the server is stale. The unload beacon
	// (SetURLStateBeacon) reads it: persisting navigation state only at
	// teardown loses it, because that IPC reply never arrives during
	// unload.
	navDirty bool
	// lastURL is the address the live page most recently navigated to, from
	// the nav event, and it is what the unload beacon writes. The cache is
	// not consulted for it: a url link's target row lives in a foreign grid
	// the cache never held, and a beacon that skipped such a view would drop
	// its navigation on tab close.
	lastURL string
	// lastTitle is the most recent page title from the nav events, for the
	// unload beacon. The freeze path gets its title from the bridge reply,
	// which the unload path cannot wait for.
	lastTitle string
}

// urlLog writes a tagged debug message to the browser console.
var urlLog = taggedLog("[urlview]")

// contentViewBounds maps a pane's screen rect to the content-box rectangle a
// hosted webview should occupy: the pane minus its border band, in CSS px
// (paneContentBox). The view fills that box; the pane carries no corner
// control, because the bar's crumb is the ascent.
func contentViewBounds(r pane.Rect) viewBounds {
	x, y, w, h := paneContentBox(r)
	return viewBounds{X: x, Y: y, W: w, H: h}
}

// urlTileForPane resolves the web-content tile a pane is descended into —
// a url tile or a serves_page tile; both go live through the same view.
func (a *App) urlTileForPane(p *pane.Pane, tileID string) (rpc.Tile, bool) {
	if g, ok := a.c.Grid(a.gridIDForPane(p)); ok {
		if t, ok := g.Tiles[tileID]; ok && t.WebContent() {
			return t, true
		}
	}
	// An off-grid, ephemeral tile is focused in the scratch grid without
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
		// ContentID is the one client-side link resolution. The door would
		// re-resolve server-side, but every content op keys by the owner.
		return rpc.PageURL(a.origin, a.contentToken, t.ContentID())
	}
	return ""
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
		// Going live is the unfreeze: the reconnect gesture clears the
		// standing intent, so the two facts never coexist. Auto-live never
		// reaches here while the intent is set, because DecideAutoLive
		// blocks it, so this only fires on the explicit reconnect click.
		tid := t.ID
		go func() {
			cleared, err := a.cl.SetURLFrozen(context.Background(), &rpc.SetURLFrozenRequest{
				TileID: tid, Frozen: false,
			})
			if err != nil {
				a.surfaceRPCError("SetTile", err)
				return
			}
			a.c.UpdateTile(cleared.GridID, *cleared)
		}()
	}
	if t.LinkTargetID == "" {
		a.placeURLView(p.ID, t)
		return
	}
	// A url link goes live as its target: the url string, the session
	// partition, the history, and the freeze writeback all belong to the
	// tile that owns the content. The target row lives in a foreign grid the
	// client has likely never loaded, so fetch it; the view places when the
	// row arrives.
	paneID := p.ID
	go func() {
		target, err := a.cl.GetTile(context.Background(), t.LinkTargetID)
		if err != nil {
			a.surfaceRPCError("GetTile", err)
			return
		}
		// The pane may have closed, ascended, or descended elsewhere while
		// the target row was in flight: the moved-on rule every async
		// descent path applies (pane.StillDescended).
		if !pane.StillDescended(a.tree.FindPane(paneID), t.ID) {
			return
		}
		a.placeURLView(paneID, *target)
	}()
}

// placeURLView places the native WebContentsView for pane paneID showing tile
// t, always the content-owning row: a link never reaches here.
func (a *App) placeURLView(paneID string, t rpc.Tile) {
	p := a.tree.FindPane(paneID)
	if p == nil {
		return
	}
	// Idempotent: the pane already shows this content live, a keep-alive
	// return, and nothing froze, so there is nothing to redo. A different
	// tile live in this pane closes through the one path that persists its
	// freeze; tearing it down elsewhere would drop the FreezeResult.
	if v := a.urlViewFor(paneID); v != nil {
		if v.tileID == t.ID {
			return
		}
		a.closeURLStream(paneID, true)
	}
	// One live surface per content tile: any other pane, at any stack level,
	// holding a live view on this content freezes now, and the opener takes
	// over. pane.TakeOver is the rule, shared with the shell side.
	for _, otherID := range pane.TakeOver(a.urlSurfaces(), paneID, t.ID) {
		a.closeURLStream(otherID, true)
	}
	r := paneRectFor(a, p)
	b := contentViewBounds(r)
	page := t.Kind != rpc.KindURL && t.ServesPage
	// Every caller places into the descent the pane is already in — the
	// descent's auto-live, the reconnect click, the promote's relocation —
	// so the pane's own frame is the descent this view belongs to. For a
	// link that frame is the link row, while t is its target.
	v := &urlView{tileID: t.ID, paneID: p.ID, descentID: p.ContentID(), bounds: b, anchor: p.Anchor(), path: slices.Clone(p.Path()), page: page}
	a.local(p.ID).urlView = v
	// durable means the descended row survives ascent: false for an
	// ephemeral visit, which gets no Freeze Page in the context menu. A page
	// view is not durable in this sense either — it carries no standing
	// freeze and no history writeback, and its frozen face is the plugin's
	// own derivation.
	durable := !page
	if tile, ok := a.descendedTile(p); ok && a.possiblyEphemeral(p, &tile) {
		durable = false
	}
	v.durable = durable
	addr := a.webAddress(&t)
	urlLog("place pane=%s tile=%s url=%s", p.ID, t.ID, addr)
	// The focus fact goes with the placement, from the same owner
	// syncURLViews reads it from: going live is not always a gesture on the
	// focused pane. The handle above is optimistic — it is set before main
	// answers, so the very next frame positions the view — so a refused
	// placement takes it back down (dropFailedURLView).
	a.bridgePlace(p.ID, t.ID, addr, b, contentZoomOf(&t), t.URLHistory, durable,
		a.liveOverlaysHidden(), p.ID == a.tree.Focus,
		func() { a.dropFailedURLView(p.ID, v) })
	a.draw()
}

// dropFailedURLView takes back the optimistic live handle when the place that
// created it was refused: main never made the view, so nothing is on screen,
// and a handle left standing would keep the pane looking live — no frozen
// preview, no re-descent through the auto-live owner, and the next successful
// place reported by the registry as a view replaced without its close. The
// teardown is only local: there is nothing native to remove, and no freeze to
// write, because no frame was ever rendered. bridgeCall has already surfaced
// the refusal. Identity-checked, since a later place may already own the pane.
func (a *App) dropFailedURLView(paneID string, v *urlView) {
	pl, ok := a.localIf(paneID)
	if !ok || pl.urlView != v {
		return
	}
	pl.urlView = nil
	a.draw()
}

// freezeTarget names the row a closing view's freeze is written to when it is
// not the view's own tile: the promote gesture (finishPromote) captures the
// ephemeral visit's final frame onto the persistent tile that replaces it.
type freezeTarget struct {
	tileID string
	gridID string
}

// closeURLStream tears down the live view for paneID: it removes the
// WebContentsView, captures a final frame, and (when freeze is true) persists
// the frozen preview, address, and title through SetURLState. An ephemeral
// tile's ascent passes freeze=false: the tile is about to be deleted, and
// freezing a row that is being discarded is work nobody will see. Idempotent.
func (a *App) closeURLStream(paneID string, freeze bool) {
	a.closeURLStreamTo(paneID, nil, freeze)
}

// closeURLStreamTo is closeURLStream with the freeze redirected to target
// (nil = the pane's own descended tile).
func (a *App) closeURLStreamTo(paneID string, target *freezeTarget, freeze bool) {
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
	if target != nil {
		tileID = target.tileID
		previewKey = target.tileID
	}
	anchor := v.anchor
	path := slices.Clone(v.path)
	urlLog("close pane=%s tile=%s", paneID, tileID)
	a.bridgeRemove(paneID, func(jpeg []byte, url, title, history string) {
		// A page view persists nothing on close: the plugin owns the frozen
		// face, deriving a thumbnail from the file, and its store has no
		// url state to write. The wildcard put below still shows the final
		// frame for the rest of the session.
		if freeze && !v.page && (len(jpeg) > 0 || url != "" || title != "") {
			gid := a.gridIDForPathFrom(anchor, path)
			if target != nil {
				gid = target.gridID
			}
			// The freeze is an in-place capture on this tile's own row —
			// nothing is shared, so there is no fork — with no claim and no
			// version bump, so a foreign writer or an auto title capture
			// racing the close cannot refuse it. A transport failure parks
			// the closure, which once the live surface is gone holds the
			// only copy of the frame, the address, and the trail. A verdict
			// surfaces and resyncs the grid: the freeze the user just saw
			// was not persisted and the preview reverts on next load. The
			// beacon form carries it through a tab close.
			req := &rpc.SetURLStateRequest{
				TileID: tileID,
				JPEG:   jpeg, URL: url, Title: title, History: history,
			}
			a.post(write{
				label: "SetURLState", gid: gid, id: tileID,
				source: "urlfreeze", failText: "page preview save failed",
				call: func(ctx context.Context) error {
					_, err := a.cl.SetURLState(ctx, req)
					if err != nil {
						urlLog("SetURLState tile=%s err=%v", tileID, err)
					}
					return err
				},
				beacon: func() (string, []byte, string) {
					path, body := rpc.SetURLStateBeacon(req)
					return path, body, rpc.BeaconJSONType
				},
			})
		}
		if len(jpeg) > 0 {
			// Reflect the just-frozen frame immediately so the pane, and
			// any mirror, shows the final state without waiting for the
			// SetURLState round trip and preview re-fetch.
			a.urlPreview.PutWildcard(previewKey, jpeg, func() { a.draw() })
		}
		a.draw()
	})
}

// freezeURLPaneByIntent runs the explicit freeze gesture, the context menu's
// "Freeze Page": persist the user's standing freeze, then tear the live view
// down through the ordinary freeze writeback. The intent lands on the
// descended row (p.ContentID()), which for a url link is the link row itself:
// the freeze is this reference's presentation, not the content owner's, and
// it is the row the next descent's DecideAutoLive reads. The intent write
// goes first, then the teardown's SetURLState capture; neither touches the
// version. Ephemeral visits are skipped — they die on ascent and carry no
// durable intent.
func (a *App) freezeURLPaneByIntent(paneID string) {
	p := a.tree.FindPane(paneID)
	pl, ok := a.localIf(paneID)
	if p == nil || !ok || pl.urlView == nil || p.ContentID() == "" {
		return
	}
	tile, ok := a.descendedTile(p)
	if !ok || tile.Kind != rpc.KindURL || a.possiblyEphemeral(p, &tile) {
		return
	}
	tid := tile.ID
	go func() {
		t, err := a.cl.SetURLFrozen(context.Background(), &rpc.SetURLFrozenRequest{
			TileID: tid, Frozen: true,
		})
		if err != nil {
			// The freeze the user asked for did not stick, so surface it;
			// the teardown below still parks the view.
			a.surfaceRPCError("SetTile", err)
		} else {
			a.c.UpdateTile(t.GridID, *t)
		}
		a.closeURLStream(paneID, true)
		a.draw()
	}()
}

// closeAllURLStreams tears down every live view. Used on beforeunload so the
// freeze writes fire before the page goes away. urlSurfaces is the snapshot,
// so closing as we go never walks a map being mutated.
func (a *App) closeAllURLStreams() {
	for _, h := range a.urlSurfaces() {
		a.closeURLStream(h.PaneID, true)
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
		p := a.tree.FindPane(paneID)
		var contentID string
		if p != nil {
			contentID = p.ContentID()
		}
		switch pane.SurfaceOf(ok, contentID, v.descentID) {
		case pane.SurfacePark:
			// Not laid out this frame: a stacked level parked behind a pane
			// tile. The level stays alive, so the view keeps running.
			a.bridgeSetHidden(paneID, true, false)
			continue
		case pane.SurfaceOrphan:
			// The pane moved on without this view's teardown. Merely hiding
			// it, as the shell twin does, would keep a Chromium page and its
			// session alive for a descent that has ended; the url side's
			// re-entry is a fresh place, so the view goes, through the one
			// path that persists its freeze. Modifies pl in place — no map
			// key changes — so the range stays valid.
			a.closeURLStream(paneID, true)
			continue
		}
		// The view fills the pane's content box, and the canvas draws the
		// parked frame into the very same box. The one bar is below every
		// pane, so no view can occlude it.
		b := contentViewBounds(r)
		v.bounds = b
		a.bridgeSetBounds(paneID, b)
		// focused feeds main's focus-steal guard (webviews.ts): only the
		// focused pane's view may take keyboard focus back after a park.
		a.bridgeSetHidden(paneID, hidden, paneID == a.tree.Focus)
	}
}

// liveOverlaysHidden reports whether live overlays (native URL views and the
// xterm shell host divs) should be parked this frame. Both kinds paint above
// the canvas and swallow mouse input over their rect, so any gesture that
// previews on the canvas — or drags a boundary across an overlay — must hide
// them first, else the overlay eats the move/up events and the gesture stalls.
func (a *App) liveOverlaysHidden() bool {
	// The url modal is DOM, and a live WebContentsView would paint over it
	// and hide what you type, so it parks the views too. The rename input
	// does not park: it opens in the bottom bar, outside every live view's
	// rect.
	return a.dragging != nil || a.rightDrag != nil || a.leftResize != nil || a.menu.IsOpen() || a.urlModalOpen
}

// isURLDescent reports whether pane p is currently descended into web
// content: a url tile or a serves_page tile, which present identically. It
// drives the input handlers' branch between Gridwell's own gestures and
// native URL interaction, and the bar slot's url-family affordances.
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
// field on a tile with the given id, driven by nav events from the bridge.
// URL tiles only: a page view's navigations stay within plugin-served
// content, and the tile row has no url_string fact to shadow.
func (a *App) updateCachedTileURL(tileID string, newURL string) {
	a.forEachCachedGrid(func(gid string, g *cache.Grid) bool {
		t, ok := g.Tiles[tileID]
		if ok && t.Kind == rpc.KindURL {
			t.URLString = newURL
			a.c.UpdateTile(gid, t)
		}
		return true
	})
}

// paneRectByID looks up the screen rect for the given pane via a fresh
// layout pass.
func (a *App) paneRectByID(paneID string) pane.Rect {
	rs := a.layoutPanes()
	return rs[paneID]
}
