//go:build js && wasm

package main

import (
	"context"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"slices"
	"sync"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/cache"
	"github.com/josephburnett/gridwell/client/contentzoom"
	"github.com/josephburnett/gridwell/client/nav"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/shellconn"
	"github.com/josephburnett/gridwell/client/traceevent"
	"github.com/josephburnett/gridwell/client/urlview"
)

// urlView is the renderer-side handle for one live URL tile, a native
// WebContentsView hosted by the Electron main process.
type urlView struct {
	tileID string
	paneID string
	// descentID is the row the pane is descended into: the link row for a
	// url link, not tileID.
	descentID string
	// anchor and path are captured at go-live for the freeze.
	anchor string
	path   []string
	// owns is urlview.Owns for the row: whether its address, title and
	// trail are the node's to write.
	owns bool
	// durable mirrors placeURLView's freeze eligibility: false for an
	// ephemeral visit, whose state a tab close must not persist.
	durable bool
	// navDirty marks a page that navigated since place. The unload beacon
	// reads it, because the teardown's IPC reply never arrives then.
	navDirty bool
	// lastURL is where an ephemeral visit's page is now, for the promote
	// gesture.
	lastURL string
	// lastTitle is for the unload beacon, which cannot wait for the bridge.
	lastTitle string
	// gen is main's view behind this handle, minted at place and kept by a
	// move; see urlview.GoneEnds.
	gen urlview.Gen
}

var (
	urlLog = taggedLog("[urlview]")
	// The state changes carry their own record; see taggedLog.
	urlConsole = consoleLog("[urlview]")
)

func contentViewBounds(r pane.Rect) viewBounds {
	x, y, w, h := paneContentBox(r)
	return viewBounds{X: x, Y: y, W: w, H: h}
}

// webAddress resolves the address a url tile presents at (urlview.Address). A
// served page's door address is never persisted, because the desktop origin is
// an ephemeral port.
func (a *App) webAddress(t *gridwellv1.Tile) string {
	if !rpc.WebContent(t) {
		return ""
	}
	return urlview.Address(rpc.PageContent(t),
		rpc.PageURL(a.origin, a.contentToken, rpc.ContentID(t)), t.UrlString)
}

// openURLStream goes live; what that does to the row is
// shellconn.DecideGoLive's.
func (a *App) openURLStream(p *pane.Pane, tileID string) {
	t, ok := a.tileForPane(p, tileID)
	if !ok || !rpc.WebContent(t) {
		return
	}
	plan, ok := shellconn.DecideGoLive(a.caps.LiveURL, t.UrlFrozen, rpc.LeafLink(t))
	if !ok {
		urlLog("live URL unavailable on this host (no Electron bridge); tile stays frozen")
		return
	}
	if plan.Unfreeze {
		a.postFrozen(t.Id, false, nil)
	}
	if plan.FollowLink {
		// The target's grid is likely never loaded, so read its row first.
		a.runGesture(nav.Gesture{Kind: nav.GestureFollowLink, PaneID: p.ID, Door: t})
		return
	}
	a.placeURLView(p.ID, t)
}

// placeURLView puts the content-owning row t live in pane paneID; engage
// decides whether to keep, move or place the view.
func (a *App) placeURLView(paneID string, t *gridwellv1.Tile) {
	p := a.tree.FindPane(paneID)
	if p == nil || !a.engage(a.urlSurface(), p, t.Id, t.Id) {
		return
	}
	v := a.urlViewIn(p, t.Id, a.ownsURLRow(t))
	v.gen = a.urlGens.Next()
	a.local(p.ID).urlView = v
	if v.owns {
		a.seedURLAddress(t)
	}
	addr := a.webAddress(t)
	a.emit(traceevent.URLOpen(p.ID, t.Id))
	urlConsole("place pane=%s tile=%s url=%s", p.ID, t.Id, addr)
	// The focus fact rides the placement, because going live is not always a
	// gesture on the focused pane.
	a.bridgePlace(p.ID, t.Id, v.gen, addr, contentViewBounds(paneRectFor(a, p)), contentzoom.Of(t.GetContentZoom()),
		t.UrlHistory, v.durable, pane.ParkSurface(a.canvasGesture(), p.ID), p.ID == a.tree.Focus,
		func() { a.dropURLView(p.ID, v) })
	a.draw()
}

// urlViewIn is the handle for tileID live in pane p.
func (a *App) urlViewIn(p *pane.Pane, tileID string, owns bool) *urlView {
	v := &urlView{tileID: tileID, paneID: p.ID, descentID: p.ContentID(), anchor: p.Anchor(),
		path: slices.Clone(p.Path()), owns: owns}
	possiblyEphemeral := false
	if tile, ok := a.descendedTile(p); ok {
		possiblyEphemeral = a.possiblyEphemeral(p, tile)
	}
	v.durable = urlview.Durable(possiblyEphemeral)
	return v
}

// ownsURLRow is urlview.Owns for row t.
func (a *App) ownsURLRow(t *gridwellv1.Tile) bool {
	accepts, known := a.gridAcceptsTiles(t.GridId)
	return urlview.Owns(rpc.PageContent(t), accepts, known)
}

// seedURLAddress files row t's address as its content entry, the basis a
// landed address is compared against, unless one is already held.
func (a *App) seedURLAddress(t *gridwellv1.Tile) {
	if _, ok := a.c.TileContent(t.Id); ok {
		return
	}
	a.c.PutFetchedContent(t.Id, []byte(t.UrlString), t.Version, a.c.AskContent(t.Id))
}

// noteLandedAddress makes the address v's page landed on its row's pending
// content when urlview.WriteAddress says so, for the content flush to post.
func (a *App) noteLandedAddress(v *urlView, landed string) {
	stored, ok := a.c.TileContent(v.tileID)
	if !ok && v.owns {
		// The entry aged out under a newer row; that row is the basis.
		if t := a.cachedTileByID(v.tileID); t != nil {
			a.seedURLAddress(t)
			stored = []byte(t.UrlString)
		}
	}
	if !urlview.WriteAddress(v.durable, v.owns, landed, string(stored)) {
		return
	}
	a.putEditedContent(v.tileID, []byte(landed))
}

// moveURLView hands the view fromID holds to pane to, page and all: no close,
// no freeze and no reload.
func (a *App) moveURLView(fromID string, to *pane.Pane) {
	from, ok := a.localIf(fromID)
	if !ok || from.urlView == nil {
		return
	}
	old := from.urlView
	from.urlView = nil
	v := a.urlViewIn(to, old.tileID, old.owns)
	v.navDirty, v.lastURL, v.lastTitle, v.gen = old.navDirty, old.lastURL, old.lastTitle, old.gen
	a.local(to.ID).urlView = v
	a.emit(traceevent.URLMove(fromID, to.ID, v.tileID))
	urlConsole("move pane=%s→%s tile=%s", fromID, to.ID, v.tileID)
	// A pane not laid out keeps the view's own bounds and stays parked.
	var b *viewBounds
	if r, ok := a.layoutPanes()[to.ID]; ok {
		cb := contentViewBounds(r)
		b = &cb
	}
	hidden := b == nil || pane.ParkSurface(a.canvasGesture(), to.ID)
	a.bridgeMove(fromID, to.ID, b, v.durable, hidden, to.ID == a.tree.Focus,
		func() { a.dropURLView(to.ID, v) })
	a.draw()
}

// dropURLView takes down a handle main has no view behind, so the pane shows
// the tile's frozen face rather than looking live over a blank.
// Identity-checked, since a later place may own the pane.
func (a *App) dropURLView(paneID string, v *urlView) {
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
// the landed address and the capture urlview.Writeback shapes.
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
	// The freeze-frame cache keys by ContentID: a link and its target share
	// one face.
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
	a.emit(traceevent.URLClose(paneID, tileID, freeze))
	urlConsole("close pane=%s tile=%s", paneID, tileID)
	a.bridgeRemove(paneID, func(jpeg []byte, url, title, history string) {
		// A promote's target was created carrying the visit's address.
		if freeze && target == nil {
			a.noteLandedAddress(v, url)
			a.flushTileContent(v.tileID)
		}
		if c, ok := urlview.Writeback(freeze, v.owns,
			urlview.Capture{JPEG: jpeg, Title: title, History: history}); ok {
			gid := a.gridIDForPathFrom(anchor, path)
			if target != nil {
				gid = target.gridID
			}
			// No claim and no version bump, so a racing foreign writer cannot
			// refuse it.
			req := &gridwellv1.SetTileRequest{TileId: tileID,
				Tile: &gridwellv1.Tile{Kind: rpc.KindURL,
					AltText: c.Title, UrlHistory: c.History},
				Preview: c.JPEG}
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
				beacon: jsonBeacon(func() (string, []byte) { return rpc.SetTileBeacon(req) }),
			})
		}
		if len(jpeg) > 0 {
			a.views.urlPreview.PutWildcard(previewKey, jpeg, func() { a.draw() })
		}
		a.draw()
	})
}

// freezeURLPaneByIntent runs the context menu's "Freeze Page". The intent lands
// on the descended row, the one DecideAutoLive reads.
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
	a.postFrozen(tile.Id, true, func() {
		a.closeURLStream(paneID, true)
		a.draw()
	})
}

// postFrozen is the one dispatcher for the standing freeze intent, keying one
// outbox entry so the last gesture wins. after runs once, after the first
// attempt, because the teardown must happen exactly once.
func (a *App) postFrozen(tileID string, frozen bool, after func()) {
	var tile *gridwellv1.Tile
	var once sync.Once
	a.post(write{
		label: "SetFrozen", gid: a.gridIDOfTile(tileID), id: tileID,
		// Its own source, because the teardown's capture reports under
		// "urlfreeze" on the same gesture and tile.
		source: "frozen", failText: "freeze state save failed",
		call: func(ctx context.Context) error {
			var err error
			tile, err = a.cl.SetFrozen(ctx, tileID, frozen)
			return err
		},
		then: func() {
			if tile != nil {
				a.c.PutWriteResponse(tile.GridId, tile, cache.WroteFrozen)
			}
		},
		done: func() {
			if after != nil {
				once.Do(after)
			}
		},
	})
}

// closeAllURLStreams tears down every live view on beforeunload.
func (a *App) closeAllURLStreams() {
	for _, h := range a.urlSurfaces() {
		a.closeURLStream(h.PaneID, true)
	}
}

// syncURLViews tracks every live view to its pane's content box each frame,
// parking the ones this frame's gesture is in the way of.
func (a *App) syncURLViews() {
	rects := a.layoutPanes()
	g := a.canvasGesture()
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
			// The pane moved on without this view's teardown; hiding it would
			// keep a Chromium page alive for a descent that ended.
			a.closeURLStream(paneID, true)
			continue
		}
		a.bridgeSetBounds(paneID, contentViewBounds(r))
		// focused feeds main's focus-steal guard in webviews.ts: only the
		// focused pane's view may take keyboard focus back after a park.
		a.bridgeSetHidden(paneID, pane.ParkSurface(g, paneID), paneID == a.tree.Focus)
	}
}

// canvasGesture mirrors this frame's gesture and overlay state for
// pane.ParkSurface and pane.CanvasOwnsPointer.
func (a *App) canvasGesture() pane.CanvasGesture {
	g := pane.CanvasGesture{
		Ghost:      a.ghost != nil,
		PaneResize: a.leftResize != nil,
		MenuOpen:   a.menu.IsOpen(),
		ModalOpen:  a.overlays.urlModalOpen,
	}
	if d := a.dragging; d != nil {
		g.DragPane = d.originPaneID
	}
	if rd := a.rightDrag; rd != nil {
		switch rd.kind {
		case rightDragSwap, rightDragSplit:
			g.PaneGesture = true
		case rightDragTileResize:
			g.TileResize = true
		case rightDragTileCenter:
			// The clone drag it becomes is a.dragging; until the ghost, it has
			// grabbed a handle in one pane.
			g.DragPane = rd.tilePaneID
		}
	}
	return g
}

// isURLDescent branches input between Gridwell's gestures and native URL
// interaction, ephemeral visits included.
func (a *App) isURLDescent(p *pane.Pane) bool {
	return a.descentKind(p) == rpc.DescentURL
}

func (a *App) paneRectByID(paneID string) pane.Rect {
	rs := a.layoutPanes()
	return rs[paneID]
}
