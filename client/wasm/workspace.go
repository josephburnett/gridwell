//go:build js && wasm

package main

// Pane-tile navigation: descending into a pane tile swaps the whole pane tree
// — descend()'s window arm, the second axis beside a pane's own frame stack.
// The bottom bar names the nesting and owns the way back out, and a debounced
// snapshot-diff persister keeps the layout blob current while inside. The
// rules live in pure packages: client/pane's Levels holds the level stack and
// the persist decision, client/wsbar the bar geometry. This file is the glue —
// gestures in, RPCs out, draw calls between.
//
// Bar gestures: a left-click on a level crumb leaves level k and everything
// deeper; a right-click renames it inline. Descent and ascent animate like
// every other tile: the zoom rides through the pane tile's footprint, and
// because the preview is the live layout under one uniform scale
// (client/panepreview's tested property), the swap lands on exactly what the
// preview showed.

import (
	"context"
	"fmt"
	"strings"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/pane"
)

// wsSaveDebounceMs is the persister's coalescing window: every
// layout-affecting gesture inside a pane tile lands in the blob at most this
// long after it settles, plus the flush on ascent. A reload inside the window
// loses at most this much arrangement.
const wsSaveDebounceMs = 500

// wsExpandState is the first-descent capture animation: the pane tile's
// screen rect at arm, growing into the level outline while the content
// underneath never moves. Drawn at the end of draw(), and cleared on install,
// where the real outline takes over seamlessly, or on a failed descent.
type wsExpandState struct {
	x, y, w, h float64
	startMs    float64
}

// wsPending coordinates a pane-tile descent's two async halves: the zoom
// animation and the tile-plus-layout fetch. The install runs when both are
// done, whichever finishes second calling maybeInstallWorkspace, and a fetch
// failure restores the origin pane's viewport once the animation lands.
// Tracked on App so thIdle can report busy between animation end and
// install.
type wsPending struct {
	animDone bool
	dataDone bool
	failed   bool
	install  func()
	restore  func()
}

// maybeInstallWorkspace runs the install, or the failure restore, once both
// halves of the descent are done. A superseded pending is ignored; input is
// blocked during the transition, so it is not expected.
func (a *App) maybeInstallWorkspace(pd *wsPending) {
	if a.wsPending != pd || !pd.animDone {
		return
	}
	if pd.failed {
		a.wsPending = nil
		a.wsExpand = nil
		pd.restore()
		a.draw()
		return
	}
	if !pd.dataDone {
		return
	}
	a.wsPending = nil
	pd.install()
}

// captureWorkspaceTree clones the current window layout as a fresh pane
// tile's initial arrangement: an encode and decode round trip through the same
// rel/abs pair the persister uses, so the capture is byte-for-byte what the
// first flush will store. One serialization owner, no second cloner. Any
// failure — a pane outside the node's reach encodes as home, per the flush
// rule, and a hard error falls all the way back — yields the single-pane
// default, because a capture must never block the descent.
func (a *App) captureWorkspaceTree(tileID, idPrefix string, origin pane.Pane) *pane.Tree {
	prefix := paneTileChainPrefix(tileID)
	data, _, err := pane.EncodeLayout(a.tree, func(id string) (string, bool) {
		rest, ok := strings.CutPrefix(id, prefix)
		return rest, ok
	})
	if err == nil {
		if t, derr := pane.DecodeLayout(data, func(id string) string { return prefix + id }, idPrefix); derr == nil {
			// An ephemeral descent — a click-visit riding the scratch grid
			// — is session state that dies on ascent. A durable capture
			// must not reference it, and a copy going live would keep the
			// outer visit's view alive past the boundary. The captured pane
			// keeps its place; the visit stays with the outer tree and
			// re-engages on ascent.
			t.Walk(func(cp *pane.Pane) {
				if cp.ContentID() == "" {
					return
				}
				if tile := a.findTileByID(cp.ContentID()); tile != nil && a.isEphemeralTile(cp, tile) {
					cp.Pop()
				}
			})
			return t
		}
	}
	return workspaceTreeFromPlace(idPrefix, origin.Anchor(), origin.Path(), origin.Cx, origin.Cy, origin.Zoom)
}

// workspaceTreeFromPlace builds the single-pane fallback: one pane at the
// given place. It is the decode-failure read-only default, the boot fallback,
// and the capture fallback when the current tree cannot encode.
func workspaceTreeFromPlace(idPrefix, anchor string, path []string, cx, cy, zoom float64) *pane.Tree {
	t := pane.NewTree()
	t.IDPrefix = idPrefix
	p := t.FocusedPane()
	p.ID = idPrefix + p.ID
	t.Focus = p.ID
	p.Stack = pane.StackAt(anchor, path, "")
	p.Cx, p.Cy = cx, cy
	if zoom <= 0 {
		zoom = 1
	}
	p.Zoom = zoom
	return t
}

// installWorkspace performs the swap: push the frame and install the decoded
// tree, with the outer level left running — its live views park off-screen
// and its shells stay attached, because liveness follows pane existence and
// no pane closed here. Level-scoped pane ids (Tree.IDPrefix) keep the
// simultaneously-alive trees from colliding in the pane-keyed maps.
// keepOuter=false means the descent has no return tree (a boot restore
// through ?w=, whose boot-blank tree is nothing the user built): the frame
// records OuterTree nil and ascent falls back to the pane tile's containing
// grid. baseline is the decoded blob bytes, nil for a never-arranged tile,
// seeding the persister's diff so a pure visit never writes.
func (a *App) installWorkspace(pt *rpc.Tile, tree *pane.Tree, originPane string, readOnly bool, baseline []byte, keepOuter bool) {
	a.transition = nil
	// The capture animation's expanding rect lands exactly where the level
	// outline draws; dropping it here is the seamless handoff.
	a.wsExpand = nil
	a.menu.Close()
	// Entering a nested level: flush the current one's layout first. Its
	// tree is about to sit un-drawn in a frame for an unbounded time, and
	// the debounce must not still be holding its latest arrangement. A no-op
	// at depth 0.
	a.flushWorkspaceSave()
	outer := a.tree
	if !keepOuter {
		// A boot restore replaces a boot-blank tree nothing lives in; the
		// ordinary descent keeps the outer level fully alive.
		outer = nil
	}

	f := pane.Level{
		OuterTree:  outer,
		OriginPane: originPane,
		TileID:     pt.ID,
		// Raw alt text: the bar substitutes the generic label at draw time,
		// so the crumb rename can round-trip an empty name honestly.
		Name:     pt.AltText,
		ReadOnly: readOnly,
	}
	pane.MarkSaved(&f, baseline)
	a.ws.Push(f)

	a.tree = tree
	a.restoreWorkspaceLeaves(tree)
	// The installed tree's focused leaf may be text-descended, from a
	// restored text_focus. Rebind the textarea singleton to it now: without
	// this the overlay keeps showing, and scroll-tracking against, whatever
	// tile it was bound to before the swap. Saves do not trust the binding,
	// but the display must not lie either.
	a.refreshFileOverlay()
	a.scheduleURLUpdate()
	a.draw()
}

// descendLevel enters a pane tile from pane p — descend()'s window arm: a
// zoom into the tile's footprint, like every descent, racing the layout
// fetch, with the swap at whichever finishes last. A blob that cannot be
// decoded installs the default read-only: the session must never overwrite a
// blob it could not read, since downgrading a newer format would rewrite
// history.
func (a *App) descendLevel(p *pane.Pane, pt *rpc.Tile) {
	originPane := p.ID
	tileID := pt.ID
	// The new level's pane-id namespace: stacked trees are all alive, and
	// pane ids key the locals, the native views, and the shell streams, so
	// each level mints and decodes under its own prefix.
	idPrefix := fmt.Sprintf("w%d:", a.ws.Depth()+1)
	// The origin pane's place, for the organize-this default and for the
	// byte-identical viewport restore under the animation.
	origin := pane.Pane{Stack: p.Stack.Clone()}
	here := p.Stack.Clone()

	pd := &wsPending{
		restore: func() {
			if fp := a.tree.FindPane(originPane); fp != nil {
				fp.Stack = origin.Stack.Clone()
			}
		},
	}
	a.wsPending = pd

	// The first descent into a never-arranged tile captures the current
	// window layout: you keep looking at exactly what you had, now inside
	// the pane tile. Its animation is the tile's face becoming the level
	// outline — an expanding teal rect, with the content never moving —
	// instead of the zoom, which would read as a jarring descend-and-return
	// over an unchanged view. The cached row picks the animation and the
	// fresh row picks the tree; they disagree only across the stale-cache
	// window, where either combination is harmless.
	if pt.BlobID == 0 {
		r := paneRectFor(a, p)
		dd := paneToDragdrop(p, r)
		x0, y0 := dd.CellToScreen(float64(pt.X), float64(pt.Y))
		x1, y1 := dd.CellToScreen(float64(pt.X+pt.W), float64(pt.Y+pt.H))
		a.wsExpand = &wsExpandState{x: x0, y: y0, w: x1 - x0, h: y1 - y0, startMs: nowMs()}
		a.startTransition(&paneTransition{
			paneID: p.ID,
			segments: []transSegment{{
				place:  &here,
				fromCx: p.Cx, fromCy: p.Cy, fromZoom: p.Zoom,
				toCx: p.Cx, toCy: p.Cy, toZoom: p.Zoom,
				durationMs: totalTransitionMs,
			}},
			onComplete: func() {
				pd.animDone = true
				a.maybeInstallWorkspace(pd)
			},
		})
	} else {
		// The zoom: pan to the tile's center while zooming until its
		// footprint fills the pane box, so the preview grows into the live
		// tree.
		r := paneRectFor(a, p)
		tcx := float64(pt.X) + float64(pt.W)/2
		tcy := float64(pt.Y) + float64(pt.H)/2
		target := textFitZoom(r, pt.W, pt.H)
		if target < p.Zoom {
			target = p.Zoom
		}
		a.startTransition(&paneTransition{
			paneID: p.ID,
			segments: []transSegment{{
				place:  &here,
				fromCx: p.Cx, fromCy: p.Cy, fromZoom: p.Zoom,
				toCx: tcx, toCy: tcy, toZoom: target,
				durationMs: totalTransitionMs,
			}},
			onComplete: func() {
				pd.animDone = true
				a.maybeInstallWorkspace(pd)
			},
		})
	}

	go func() {
		// Refetch the tile rather than trusting the cached row: a stale
		// BlobID of 0 — another client's first arrange whose echo has not
		// landed here yet — would install the writable default, and the
		// persister could then overwrite the fresh arrangement, since
		// layout writes carry no version bump to conflict on. One RPC
		// closes the window to genuine concurrent edits.
		fresh, err := a.cl.GetTile(context.Background(), tileID)
		if err != nil {
			a.surfaceRPCError("GetTile", err)
			pd.failed = true
			a.maybeInstallWorkspace(pd)
			return
		}
		if fresh.LinkTargetID != "" {
			// A pane link opens the target's arrangement, the one shared
			// layout, and the persister then writes back through the target
			// id too. The same read-through rule as every other content
			// door.
			tileID = fresh.LinkTargetID
			fresh, err = a.cl.GetTile(context.Background(), tileID)
			if err != nil {
				a.surfaceRPCError("GetTile", err)
				pd.failed = true
				a.maybeInstallWorkspace(pd)
				return
			}
		}
		var tree *pane.Tree
		var data []byte
		readOnly := false
		capture := false
		if fresh.BlobID == 0 {
			// Never arranged: the first descent captures the current window
			// layout. Deferred to install time so the encode reads the tree
			// as it stands at the swap, after pd.restore().
			capture = true
		} else {
			data, _, _, err = a.cl.ReadContent(context.Background(), tileID)
			if err != nil {
				a.surfaceRPCError("ReadContent", err)
				pd.failed = true
				a.maybeInstallWorkspace(pd)
				return
			}
			prefix := paneTileChainPrefix(tileID)
			var derr error
			tree, derr = pane.DecodeLayout(data, func(id string) string { return prefix + id }, idPrefix)
			if derr != nil {
				a.reportErr(errsurface.Error, "layout:"+tileID,
					"workspace layout unreadable — opened read-only: "+derr.Error())
				tree = workspaceTreeFromPlace(idPrefix, origin.Anchor(), origin.Path(), origin.Cx, origin.Cy, origin.Zoom)
				readOnly = true
			}
		}
		pd.install = func() {
			// The animation left the origin pane zoomed into the tile: put
			// its true place back before capturing the outer tree, so
			// ascent restores exactly what the user left.
			pd.restore()
			if capture {
				tree = a.captureWorkspaceTree(tileID, idPrefix, origin)
			}
			a.installWorkspace(fresh, tree, originPane, readOnly, data, true)
		}
		pd.dataDone = true
		a.maybeInstallWorkspace(pd)
	}()
}

// bootWorkspace restores the innermost pane tile from a reload (?w=). The
// outer tree is nil by design: nesting membership is session-only, like the
// outer frames of a pane's place. The defaults — a never-arranged tile, an
// unreadable blob — open on the pane tile's containing grid, centered on the
// tile.
func (a *App) bootWorkspace(tileID string) {
	tile, err := a.cl.GetTile(context.Background(), tileID)
	if err != nil {
		a.surfaceRPCError("GetTile", err)
		return
	}
	if tile.LinkTargetID != "" {
		// A pane link boots the target's arrangement (see openWorkspace).
		tileID = tile.LinkTargetID
		tile, err = a.cl.GetTile(context.Background(), tileID)
		if err != nil {
			a.surfaceRPCError("GetTile", err)
			return
		}
	}
	if !rpc.IsWorkspaceKind(tile.Kind) {
		a.reportErr(errsurface.Error, "layout:"+tileID, "?w= names a non-workspace tile")
		return
	}
	// The boot pane tile is level 1: the stack is empty at boot.
	homeTree := func() *pane.Tree {
		return workspaceTreeFromPlace("w1:", tile.GridID, nil,
			float64(tile.X)+float64(tile.W)/2, float64(tile.Y)+float64(tile.H)/2, 1)
	}
	if tile.BlobID == 0 {
		a.installWorkspace(tile, homeTree(), "", false, nil, false)
		return
	}
	data, _, _, err := a.cl.ReadContent(context.Background(), tileID)
	if err != nil {
		a.surfaceRPCError("ReadContent", err)
		return
	}
	prefix := paneTileChainPrefix(tileID)
	tree, derr := pane.DecodeLayout(data, func(id string) string { return prefix + id }, "w1:")
	readOnly := false
	if derr != nil {
		a.reportErr(errsurface.Error, "layout:"+tileID,
			"workspace layout unreadable — opened read-only: "+derr.Error())
		tree = homeTree()
		readOnly = true
	}
	a.installWorkspace(tile, tree, "", readOnly, data, false)
}

// restoreWorkspaceLeaves applies the boot-blank fixups a freshly-installed
// tree needs: an empty anchor means the node's home grid, exactly as the boot
// pane resolves it, and every leaf's grid fetch is kicked so the panes fill
// in. Loose, per the urlwalk rule: a place that no longer resolves stays
// where its longest live prefix lands, and gridIDForPane already walks
// loosely.
func (a *App) restoreWorkspaceLeaves(tree *pane.Tree) {
	tree.Walk(func(p *pane.Pane) {
		if p.Anchor() == "" {
			p.Reset(pane.Frame{GridID: a.home, Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom})
		}
		if p.Zoom == 0 {
			p.Zoom = 1
		}
		a.fetchGrid(a.gridIDForPane(p))
		if p.ContentID() != "" {
			a.autoLiveOnRestore(p.ID, p.ContentID())
		}
	})
}

// ascendLevels leaves `count` window levels: the pane-tile axis of the same
// pop, whose pane-frame axis is ascend(). For each level it flushes the layout
// one last time, freezes and forgets the inner leaves, pops, and restores the
// outer tree verbatim, with focus returning to the origin pane. The final
// landing animates the reverse of the descent — the pane tile's face
// shrinking back to the origin pane's viewport. A frame with no outer tree,
// from a boot restore, falls back to a fresh pane at the pane tile's
// containing grid, the same degradation an ascent has after a reload.
func (a *App) ascendLevels(count int) {
	if count <= 0 {
		return
	}
	for i := 0; i < count; i++ {
		top := a.ws.Top()
		if top == nil {
			return
		}
		a.flushWorkspaceSave()
		a.transition = nil
		a.menu.Close()
		a.flushDroppedSubtree(a.tree.Root)
		f, _ := a.ws.Pop()
		if f.OuterTree != nil {
			a.tree = f.OuterTree
			if f.OriginPane != "" && a.tree.FindPane(f.OriginPane) != nil {
				a.tree.Focus = f.OriginPane
			}
			if i == count-1 {
				a.animateWorkspaceReturn(f)
			}
		} else {
			a.tree = a.fallbackTreeFor(f.TileID)
		}
	}
	// Same rebind as installWorkspace: the restored outer tree's focused pane
	// may itself be text-descended, and the singleton must follow the swap.
	a.refreshFileOverlay()
	// The restored outer leaves never froze — the boundary keeps every
	// level alive — so for a still-running pane this walk is a no-op, since
	// the stream openers are idempotent. It matters for the panes that lost
	// their surface to the one-surface rule while a higher level held the
	// same tile: the holder just closed, so the surface is free again and
	// the pane re-engages, through the one owner of that decision.
	for _, h := range pane.ContentPanes(a.tree) {
		a.autoLiveOnRestore(h.PaneID, h.TileID)
	}
	a.scheduleURLUpdate()
	a.draw()
}

// animateWorkspaceReturn plays the ascent's zoom-out: the origin pane starts
// zoomed into the pane tile's footprint, the reverse of the descent's end,
// and animates back to its restored viewport. Skipped, for an instant
// landing, when the tile row is not in the cached grid: there is nothing to
// zoom out of.
func (a *App) animateWorkspaceReturn(f pane.Level) {
	p := a.tree.FindPane(f.OriginPane)
	if p == nil || p.ContentID() != "" {
		return
	}
	g, ok := a.c.Grid(a.gridIDForPane(p))
	if !ok {
		return
	}
	t, ok := g.Tiles[f.TileID]
	if !ok {
		return
	}
	r := paneRectFor(a, p)
	tcx := float64(t.X) + float64(t.W)/2
	tcy := float64(t.Y) + float64(t.H)/2
	overtake := textFitZoom(r, t.W, t.H)
	savedCx, savedCy, savedZoom := p.Cx, p.Cy, p.Zoom
	here := p.Stack.Clone()
	if overtake < savedZoom {
		overtake = savedZoom
	}
	a.startTransition(&paneTransition{
		paneID: p.ID,
		segments: []transSegment{{
			place:  &here,
			fromCx: tcx, fromCy: tcy, fromZoom: overtake,
			toCx: savedCx, toCy: savedCy, toZoom: savedZoom,
			durationMs: totalTransitionMs,
		}},
	})
}

// The bar itself — always on, carrying the level crumbs and the focused
// pane's descent chain — lives in bottombar.go.

// commitWorkspaceRename posts the user-owned name for the pane tile at
// `level` and updates its crumb and version claim from the response. A rename
// bumps the tile version, so the layout persister's next write carries the
// fresh claim rather than burning a conflict retry.
func (a *App) commitWorkspaceRename(level int, alt string) {
	f := a.ws.At(level)
	if f == nil {
		return
	}
	tileID := f.TileID
	a.commitRenameRetained(tileID, alt, func(tile *rpc.Tile) {
		// The frame may be gone by the time a parked retry lands, if the
		// user left the level. The rename still landed on the tile row;
		// only the crumb update is conditional.
		if fr := a.ws.At(level); fr != nil && fr.TileID == tileID {
			fr.Name = tile.AltText
		}
		a.c.UpdateTile(tile.GridID, *tile)
	})
}

// fallbackTreeFor builds the post-reload ascent landing: a fresh single pane
// that re-anchors to the pane tile's containing grid, its viewport centered
// on the tile. The GetTile rides a goroutine, because this runs inside a
// click callback where a blocking network call would wedge the wasm
// scheduler, so the pane lands at home for a frame and re-anchors when the
// tile arrives. An unreachable tile leaves it at home.
func (a *App) fallbackTreeFor(tileID string) *pane.Tree {
	t := pane.NewTree()
	p := t.FocusedPane()
	p.Reset(pane.Frame{GridID: a.home, Zoom: 1})
	paneID := p.ID
	go func() {
		tile, err := a.cl.GetTile(context.Background(), tileID)
		if err != nil {
			a.surfaceRPCError("GetTile", err)
			return
		}
		// Re-anchor only if the pane is still sitting untouched at home —
		// a user who already navigated wins over the late fetch.
		fp := a.tree.FindPane(paneID)
		if fp == nil || fp.Anchor() != a.home || len(fp.Path()) > 0 || fp.ContentID() != "" {
			return
		}
		fp.Reset(pane.Frame{GridID: tile.GridID,
			Cx:   float64(tile.X) + float64(tile.W)/2,
			Cy:   float64(tile.Y) + float64(tile.H)/2,
			Zoom: fp.Zoom})
		a.fetchGrid(tile.GridID)
		a.scheduleURLUpdate()
		a.draw()
	}()
	return t
}

// ── the persister ──────────────────────────────────────────────────────────

// scheduleWorkspaceSave arms the debounced layout persister. Called from
// draw() whenever the level stack is non-empty: the layout blob is derived
// from the live tree by encode and diff, so there is no per-gesture
// persistence hook to forget.
func (a *App) scheduleWorkspaceSave() {
	if a.ws.Depth() == 0 {
		return
	}
	a.sched.wsSave.arm(wsSaveDebounceMs)
}

// flushWorkspaceSave persists the current layout immediately if it changed
// (the ascent-boundary flush; also the debounce callback's body).
func (a *App) flushWorkspaceSave() {
	top := a.ws.Top()
	if top == nil {
		return
	}
	prefix := paneTileChainPrefix(top.TileID)
	data, skipped, err := pane.EncodeLayout(a.tree, func(id string) (string, bool) {
		rest, ok := strings.CutPrefix(id, prefix)
		return rest, ok
	})
	if err != nil {
		a.reportErr(errsurface.Error, "layout:"+top.TileID, "workspace layout encode failed: "+err.Error())
		return
	}
	if len(skipped) > 0 {
		// A pane looking outside the owning node's reach persists as home.
		// One coalesced notice, on the same source key, not one per save.
		a.reportErr(errsurface.Info, "layout:"+top.TileID,
			"a pane views content the workspace's node cannot reach; it will reopen at home")
	}
	if !pane.ShouldPersist(top, data) {
		return
	}
	go a.postPaneLayout(top.TileID, data)
}

// postPaneLayout sends one layout write through WriteContent, the one content
// door. A pane layout is framing-class: no version claim and no bump, so the
// frame carries no version to track and there is no conflict to re-claim
// through. Success marks the bytes saved and lets the response row fan into
// the cache exactly like an event, through the one Apply owner. A transport
// failure parks the encoded layout: inside the level the debounce diff
// retries naturally, but the ascent-boundary flush fires once and then pops
// the frame, so by the time the error lands the inner tree is gone and `data`
// is the only copy of the arrangement. The beacon form carries it through a
// tab close.
func (a *App) postPaneLayout(tileID string, data []byte) {
	var tile *rpc.Tile
	a.do(write{
		label: "PaneLayout", gid: a.gridIDOfTile(tileID), id: tileID,
		source: "layout:" + tileID, failText: "workspace layout unsaved",
		call: func(ctx context.Context) error {
			var err error
			tile, err = a.cl.WriteContent(ctx, tileID, 0, data)
			return err
		},
		then: func() {
			if top := a.ws.Top(); top != nil && top.TileID == tileID {
				pane.MarkSaved(top, data)
			}
			a.c.Apply(rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: *tile}})
			a.resolveErr("rpc:PaneLayout")
		},
		beacon: func() (string, []byte, string) {
			path, body := rpc.WriteContentBeacon(tileID, 0, data)
			return path, body, rpc.BeaconStreamType
		},
	})
}
