//go:build js && wasm

package main

import (
	"google.golang.org/protobuf/proto"

	"context"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"slices"
	"sync"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/cache"
	"github.com/josephburnett/gridwell/client/nav"
	"github.com/josephburnett/gridwell/client/pane"
)

// urlView is the renderer-side handle for one live URL tile, a native
// WebContentsView hosted by the Electron main process. nil means no live URL
// descent.
type urlView struct {
	tileID string
	paneID string
	// descentID is the row the pane is descended into, the link row for a url
	// link and not tileID. The per-frame sweep compares it against the pane's
	// descent to spot one that moved on.
	descentID string
	bounds    viewBounds
	// anchor and path are captured at go-live, because the freeze needs them
	// to resolve this tile's leaf grid.
	anchor string
	path   []string
	// page marks a serves_page view, whose close skips the freeze writeback:
	// the owning plugin derives its frozen face and stores nothing.
	page bool
	// durable mirrors placeURLView's freeze eligibility: false for a page
	// view or an ephemeral visit, whose state a tab close must not persist.
	durable bool
	// navDirty marks a page that navigated since place. The unload beacon
	// reads it, because the teardown's IPC reply never arrives then.
	navDirty bool
	// lastURL is what the unload beacon writes, since a url link's target row
	// lives in a grid the cache never held.
	lastURL string
	// lastTitle is for the unload beacon, which cannot wait for the bridge
	// reply the freeze path reads its title from.
	lastTitle string
}

var urlLog = taggedLog("[urlview]")

// contentViewBounds is the content-box rectangle a hosted webview occupies,
// in CSS px.
func contentViewBounds(r pane.Rect) viewBounds {
	x, y, w, h := paneContentBox(r)
	return viewBounds{X: x, Y: y, W: w, H: h}
}

// urlTileForPane resolves the web-content tile a pane is descended into. A
// url tile and a serves_page tile share one view.
func (a *App) urlTileForPane(p *pane.Pane, tileID string) (*gridwellv1.Tile, bool) {
	if g, ok := a.c.Grid(a.gridIDForPane(p)); ok {
		if t, ok := g.Tiles[tileID]; ok && rpc.WebContent(t) {
			return t, true
		}
	}
	// An ephemeral tile is focused in the scratch grid without re-anchoring
	// the pane, so resolve by id from any cached grid.
	if t := a.findTileByID(tileID); t != nil && rpc.WebContent(t) {
		return t, true
	}
	return nil, false
}

// webAddress resolves the address a web-content tile presents at. A
// serves_page tile's door address is derived at use time, never persisted,
// because the desktop origin is an ephemeral port.
func (a *App) webAddress(t *gridwellv1.Tile) string {
	if t.Kind == rpc.KindURL {
		return t.UrlString
	}
	if rpc.PageContent(t) {
		// Every content op keys by the owner.
		return rpc.PageURL(a.origin, a.contentToken, rpc.ContentID(t))
	}
	return ""
}

// openURLStream goes live: main places a native WebContentsView for (pane,
// tile). No-op outside Electron.
func (a *App) openURLStream(p *pane.Pane, tileID string) {
	if !a.caps.LiveURL {
		urlLog("live URL unavailable on this host (no Electron bridge); tile stays frozen")
		return
	}
	t, ok := a.urlTileForPane(p, tileID)
	if !ok {
		return
	}
	if t.UrlFrozen {
		// Going live is the unfreeze, so the two facts never coexist. This
		// only fires on the explicit reconnect click, since DecideAutoLive
		// blocks auto-live while the intent is set.
		a.postURLFrozen(t.Id, false, nil)
	}
	if !rpc.LeafLink(t) {
		a.placeURLView(p.ID, t)
		return
	}
	// A url link goes live as its target, which owns the url string, session
	// partition, history and freeze writeback. That row is read first, since
	// its grid is likely never loaded.
	a.runGesture(nav.Gesture{Kind: nav.GestureFollowLink, PaneID: p.ID, Door: t})
}

// placeURLView places the native WebContentsView for pane paneID showing
// tile t, always the content-owning row: a link never reaches here.
func (a *App) placeURLView(paneID string, t *gridwellv1.Tile) {
	p := a.tree.FindPane(paneID)
	if p == nil {
		return
	}
	// A different tile closes through the one path that persists its
	// freeze.
	if v := a.urlViewFor(paneID); v != nil {
		if v.tileID == t.Id {
			return
		}
		a.closeURLStream(paneID, true)
	}
	// One live surface per content tile: pane.TakeOver names every other
	// pane to freeze, and the rule is shared with the shell side.
	for _, otherID := range pane.TakeOver(a.urlSurfaces(), paneID, t.Id) {
		a.closeURLStream(otherID, true)
	}
	r := paneRectFor(a, p)
	b := contentViewBounds(r)
	page := rpc.PageContent(t)
	// Every caller places into the descent the pane is already in, so the
	// pane's frame is this view's descent.
	v := &urlView{tileID: t.Id, paneID: p.ID, descentID: p.ContentID(), bounds: b, anchor: p.Anchor(), path: slices.Clone(p.Path()), page: page}
	a.local(p.ID).urlView = v
	// durable means the descended row survives ascent. A page view is not:
	// it carries no standing freeze and no history writeback.
	durable := !page
	if tile, ok := a.descendedTile(p); ok && a.possiblyEphemeral(p, tile) {
		durable = false
	}
	v.durable = durable
	addr := a.webAddress(t)
	urlLog("place pane=%s tile=%s url=%s", p.ID, t.Id, addr)
	// The focus fact rides the placement, because going live is not always a
	// gesture on the focused pane. The handle is set before main answers, so
	// a refusal takes it back down.
	a.bridgePlace(p.ID, t.Id, addr, b, contentZoomOf(t), t.UrlHistory, durable,
		a.liveOverlaysHidden(), p.ID == a.tree.Focus,
		func() { a.dropFailedURLView(p.ID, v) })
	a.draw()
}

// dropFailedURLView takes back the optimistic handle when the place was
// refused, since one left standing keeps the pane looking live with no frozen
// preview. Identity-checked, since a later place may own the pane.
func (a *App) dropFailedURLView(paneID string, v *urlView) {
	pl, ok := a.localIf(paneID)
	if !ok || pl.urlView != v {
		return
	}
	pl.urlView = nil
	a.draw()
}

// freezeTarget names the row a closing view's freeze is written to when it
// is not the view's own tile, as the promote gesture does.
type freezeTarget struct {
	tileID string
	gridID string
}

// closeURLStream tears the live view down and, when freeze is true, persists
// the frozen preview, address and title. An ephemeral tile's ascent passes
// false, since the row is about to be deleted.
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
	// The freeze-frame cache is read by ContentID, since a link and its
	// target share one face.
	previewKey := tileID
	if ct := a.cachedTileByID(tileID); ct != nil {
		previewKey = rpc.ContentID(ct)
	}
	if target != nil {
		tileID = target.tileID
		previewKey = target.tileID
	}
	anchor := v.anchor
	path := slices.Clone(v.path)
	urlLog("close pane=%s tile=%s", paneID, tileID)
	a.bridgeRemove(paneID, func(jpeg []byte, url, title, history string) {
		// A page view persists nothing: the plugin owns its frozen face.
		if freeze && !v.page && (len(jpeg) > 0 || url != "" || title != "") {
			gid := a.gridIDForPathFrom(anchor, path)
			if target != nil {
				gid = target.gridID
			}
			// No claim and no version bump, so a foreign writer racing the
			// close cannot refuse it. Once the surface is gone this closure
			// holds the only copy of the capture.
			req := &gridwellv1.SetTileRequest{TileId: tileID,
				Tile: &gridwellv1.Tile{Kind: rpc.KindURL,
					UrlString: url, AltText: title, UrlHistory: history},
				Preview: jpeg}
			a.post(write{
				label: "SetURLState", gid: gid, id: tileID,
				source: "urlfreeze", failText: "page preview save failed",
				call: func(ctx context.Context) error {
					_, err := a.cl.SetTile(ctx, req)
					if err != nil {
						urlLog("SetURLState tile=%s err=%v", tileID, err)
					}
					return err
				},
				beacon: func() (string, []byte, string) {
					path, body := rpc.SetTileBeacon(req)
					return path, body, rpc.BeaconJSONType
				},
			})
		}
		if len(jpeg) > 0 {
			// Show the final state without waiting for the round trip.
			a.views.urlPreview.PutWildcard(previewKey, jpeg, func() { a.draw() })
		}
		a.draw()
	})
}

// freezeURLPaneByIntent runs the context menu's "Freeze Page". The intent
// lands on the descended row, the link row for a url link, because the freeze
// is that reference's presentation and it is the row DecideAutoLive reads.
// Ephemeral visits carry no durable intent.
func (a *App) freezeURLPaneByIntent(paneID string) {
	p := a.tree.FindPane(paneID)
	pl, ok := a.localIf(paneID)
	if p == nil || !ok || pl.urlView == nil || p.ContentID() == "" {
		return
	}
	tile, ok := a.descendedTile(p)
	if !ok || tile.Kind != rpc.KindURL || a.possiblyEphemeral(p, tile) {
		return
	}
	a.postURLFrozen(tile.Id, true, func() {
		// A freeze still owed to the server is the outbox's business, so the
		// teardown runs whatever the write did.
		a.closeURLStream(paneID, true)
		a.draw()
	})
}

// postURLFrozen is the one dispatcher for the standing freeze intent, both
// directions keying the same outbox entry so the last gesture wins. after
// runs once the first attempt finishes, because the teardown must happen
// exactly once however often the write is retried.
func (a *App) postURLFrozen(tileID string, frozen bool, after func()) {
	var tile *gridwellv1.Tile
	var once sync.Once
	a.post(write{
		label: "SetURLFrozen", gid: a.gridIDOfTile(tileID), id: tileID,
		// Its own source, because the teardown's capture reports under
		// "urlfreeze" on the same gesture and tile.
		source: "urlfrozen", failText: "freeze state save failed",
		call: func(ctx context.Context) error {
			var err error
			tile, err = a.cl.SetURLFrozen(ctx, tileID, frozen)
			return err
		},
		then: func() {
			if tile != nil {
				a.c.UpdateTile(tile.GridId, tile)
			}
		},
		done: func() {
			if after != nil {
				once.Do(after)
			}
		},
	})
}

// closeAllURLStreams tears down every live view on beforeunload, so the
// freeze writes fire before the page goes.
func (a *App) closeAllURLStreams() {
	for _, h := range a.urlSurfaces() {
		a.closeURLStream(h.PaneID, true)
	}
}

// syncURLViews tracks every live view to its pane's content box each frame,
// parking it during gestures that paint canvas overlays on top.
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
			// A stacked level parked behind a pane tile stays alive.
			a.bridgeSetHidden(paneID, true, false)
			continue
		case pane.SurfaceOrphan:
			// The pane moved on without this view's teardown. Hiding it, as
			// the shell twin does, would keep a Chromium page alive for a
			// descent that ended.
			a.closeURLStream(paneID, true)
			continue
		}
		// The canvas draws the parked frame into the very same box.
		b := contentViewBounds(r)
		v.bounds = b
		a.bridgeSetBounds(paneID, b)
		// focused feeds main's focus-steal guard in webviews.ts: only the
		// focused pane's view may take keyboard focus back after a park.
		a.bridgeSetHidden(paneID, hidden, paneID == a.tree.Focus)
	}
}

// liveOverlaysHidden reports whether live overlays park this frame. They
// swallow mouse input over their rect, so a gesture that previews on the
// canvas must hide them first.
func (a *App) liveOverlaysHidden() bool {
	// The url modal is DOM and a live view would paint over it. The rename
	// input opens in the bar, outside every live view's rect.
	return a.dragging != nil || a.rightDrag != nil || a.leftResize != nil || a.menu.IsOpen() || a.overlays.urlModalOpen
}

// isURLDescent branches input between Gridwell's gestures and native URL
// interaction. descentKind resolves an ephemeral url visit too, so live-url
// input handling works for it.
func (a *App) isURLDescent(p *pane.Pane) bool {
	return a.descentKind(p) == rpc.DescentURL
}

// updateCachedTileURL rewrites UrlString on a tile, driven by the bridge's
// nav events. URL tiles only: a page row has no url_string fact to shadow.
func (a *App) updateCachedTileURL(tileID string, newURL string) {
	a.forEachCachedGrid(func(gid string, g *cache.Grid) bool {
		t, ok := g.Tiles[tileID]
		if ok && t.Kind == rpc.KindURL {
			// cache.Grid hands out the cached rows themselves, so patch a
			// clone through UpdateTile.
			patched := proto.CloneOf(t)
			patched.UrlString = newURL
			a.c.UpdateTile(gid, patched)
		}
		return true
	})
}

func (a *App) paneRectByID(paneID string) pane.Rect {
	rs := a.layoutPanes()
	return rs[paneID]
}
