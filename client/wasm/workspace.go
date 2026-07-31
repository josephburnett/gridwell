//go:build js && wasm

package main

// Workspace navigation: descending into a pane tile swaps the whole pane
// tree (the THIRD descent verb — neither a path push nor a portal anchor
// swap), the bottom bar names the nesting and owns the way back out, and a
// debounced snapshot-diff persister keeps the layout blob current while
// inside. The rules live in pure packages — client/workspace (the stack and
// the write decision) and client/wsbar (the bar geometry) — this file is
// the glue: gestures in, RPCs out, draw calls between.
//
// Bar gestures: LEFT-click a workspace crumb LEAVES workspace k and
// everything deeper; RIGHT-click renames it inline (issues #212, #220).
// Descent and ascent animate like every other tile: the
// zoom rides through the pane tile's footprint, and because the preview is
// the live layout under one uniform scale (client/panepreview's tested
// property), the swap lands on exactly what the preview showed.

import (
	"context"
	"slices"
	"strings"
	"syscall/js"

	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/workspace"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// wsSaveDebounceMs is the persister's coalescing window: every layout-
// affecting gesture inside a workspace lands in the blob at most this long
// after it settles (plus the flush on ascent). A reload inside the window
// loses at most this much arrangement.
const wsSaveDebounceMs = 500

// wsPending coordinates a workspace descent's two async halves: the zoom
// animation and the tile+layout fetch. The install runs when BOTH are done
// (whichever finishes second calls maybeInstallWorkspace); a fetch failure
// restores the origin pane's viewport once the animation lands. Tracked on
// App so thIdle can report busy between animation end and install.
type wsPending struct {
	animDone bool
	dataDone bool
	failed   bool
	install  func()
	restore  func()
}

// maybeInstallWorkspace runs the install (or the failure restore) once both
// halves of the descent are done. Superseded pendings (never expected —
// input is blocked during the transition) are ignored.
func (a *App) maybeInstallWorkspace(pd *wsPending) {
	if a.wsPending != pd || !pd.animDone {
		return
	}
	if pd.failed {
		a.wsPending = nil
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

// startWorkspaceDescent enters a pane tile from pane p: a zoom into the
// tile's footprint (like every descent) racing the layout fetch, with the
// swap at whichever finishes last. A blob that cannot be decoded installs
// the default READ-ONLY: the session must never overwrite a blob it could
// not read (a newer format downgraded would be rewriting history). A
// never-arranged tile opens on ITS CONTAINING GRID — dropping a workspace
// into a grid means "organize this", so entering it shows the place it
// lives, exactly as the descending pane saw it (owner decision 2026-07-10).
func (a *App) startWorkspaceDescent(p *pane.Pane, pt *rpc.Tile) {
	originPane := p.ID
	tileID := pt.ID
	// The origin pane's place, for the organize-this default and for the
	// byte-identical viewport restore under the animation.
	origin := pane.Pane{Anchor: p.Anchor, Path: slices.Clone(p.Path), Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom}

	pd := &wsPending{
		restore: func() {
			if fp := a.tree.FindPane(originPane); fp != nil {
				fp.Cx, fp.Cy, fp.Zoom = origin.Cx, origin.Cy, origin.Zoom
			}
		},
	}
	a.wsPending = pd

	// The zoom: pan to the tile's center while zooming until its footprint
	// fills the pane box — the preview grows into the live workspace.
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
			path:   slices.Clone(p.Path),
			fromCx: p.Cx, fromCy: p.Cy, fromZoom: p.Zoom,
			toCx: tcx, toCy: tcy, toZoom: target,
			durationMs: totalTransitionMs,
		}},
		onComplete: func() {
			pd.animDone = true
			a.maybeInstallWorkspace(pd)
		},
	})

	go func() {
		// Refetch the tile rather than trusting the cached row: a stale
		// BlobID of 0 (another client's first arrange whose echo hasn't
		// landed here yet) would install the WRITABLE default, and the
		// persister could then overwrite the fresh arrangement — layout
		// writes carry no version bump to conflict on. One RPC closes the
		// window to genuine concurrent edits (the I11/#5 residual class).
		fresh, err := a.cl.GetTile(context.Background(), tileID)
		if err != nil {
			a.surfaceRPCError("GetTile", err)
			pd.failed = true
			a.maybeInstallWorkspace(pd)
			return
		}
		if fresh.LinkTargetID != "" {
			// A pane LINK opens the TARGET workspace — the one shared
			// arrangement; the persister then writes back through the target
			// id too. Same read-through rule as every other content door.
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
		if fresh.BlobID == 0 {
			tree = workspaceTreeFromPlace(origin.Anchor, origin.Path, origin.Cx, origin.Cy, origin.Zoom)
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
			tree, derr = pane.DecodeLayout(data, func(id string) string { return prefix + id })
			if derr != nil {
				a.reportErr(errsurface.Error, "layout:"+tileID,
					"workspace layout unreadable — opened read-only: "+derr.Error())
				tree = workspaceTreeFromPlace(origin.Anchor, origin.Path, origin.Cx, origin.Cy, origin.Zoom)
				readOnly = true
			}
		}
		pd.install = func() {
			// The animation left the origin pane zoomed into the tile; put
			// its true place back BEFORE capturing the outer tree, so ascent
			// restores exactly what the user left (the roundtrip spec's
			// byte-identical assertion rides on this).
			pd.restore()
			a.installWorkspace(fresh, tree, originPane, readOnly, data, true)
		}
		pd.dataDone = true
		a.maybeInstallWorkspace(pd)
	}()
}

// workspaceTreeFromPlace builds the fresh-workspace default: one pane at the
// given place. For a descent that is the origin pane's own view — "organize
// THIS grid" — and for a boot fallback, the pane tile's containing grid.
func workspaceTreeFromPlace(anchor string, path []string, cx, cy, zoom float64) *pane.Tree {
	t := pane.NewTree()
	p := t.FocusedPane()
	p.Anchor = anchor
	p.Path = slices.Clone(path)
	p.Cx, p.Cy = cx, cy
	if zoom <= 0 {
		zoom = 1
	}
	p.Zoom = zoom
	return t
}

// installWorkspace performs the actual swap: flush and forget every outer
// leaf (reload semantics at the boundary — shells detach to tmux, live urls
// freeze, text saves; pane ids collide between trees so locals must empty),
// push the frame, install the decoded tree. keepOuter=false means the
// descent has no return tree (boot restore via ?w= — the boot-blank tree is
// nothing the user built): the frame records OuterTree nil and ascent falls
// back to the pane tile's containing grid. baseline is the decoded blob
// bytes (nil for a never-arranged tile), seeding the persister's diff so a
// pure visit never writes.
func (a *App) installWorkspace(pt *rpc.Tile, tree *pane.Tree, originPane string, readOnly bool, baseline []byte, keepOuter bool) {
	a.transition = nil
	a.menu.Close()
	// Entering a NESTED workspace: flush the current one's layout first —
	// its tree is about to sit un-drawn in a frame for an unbounded time,
	// and the debounce must not still be holding its latest arrangement.
	// A no-op at depth 0 (no workspace to flush).
	a.flushWorkspaceSave()
	outer := a.tree
	a.flushDroppedSubtree(outer.Root)
	if !keepOuter {
		outer = nil
	}

	f := workspace.Frame{
		OuterTree:   outer,
		OriginPane:  originPane,
		TileID:      pt.ID,
		TileVersion: pt.Version,
		// Raw alt: the bar substitutes the generic label at draw time, so
		// the crumb rename can round-trip an empty name honestly.
		Name:     pt.AltText,
		ReadOnly: readOnly,
	}
	workspace.MarkSaved(&f, baseline)
	a.ws.Push(f)

	a.tree = tree
	a.restoreWorkspaceLeaves(tree)
	// The installed tree's focused leaf may be text-descended (a restored
	// text_focus). Rebind the textarea singleton to it NOW: without this the
	// overlay keeps showing — and scroll-tracking against — whatever tile it
	// was bound to before the swap, which is exactly the stale-binding state
	// the 2026-07-18 stomp rode in on. (Saves no longer trust the binding,
	// but the DISPLAY must not lie either.)
	a.refreshFileOverlay()
	a.scheduleURLUpdate()
	a.draw()
}

// bootWorkspace restores the innermost workspace from a reload (?w=). The
// outer tree is nil by design — nesting membership is session-only, like
// portal Up frames. Defaults (never-arranged / unreadable blob) open on the
// pane tile's containing grid, centered on the tile.
func (a *App) bootWorkspace(tileID string) {
	tile, err := a.cl.GetTile(context.Background(), tileID)
	if err != nil {
		a.surfaceRPCError("GetTile", err)
		return
	}
	if tile.LinkTargetID != "" {
		// A pane LINK boots the TARGET workspace (see openWorkspace).
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
	homeTree := func() *pane.Tree {
		return workspaceTreeFromPlace(tile.GridID, nil,
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
	tree, derr := pane.DecodeLayout(data, func(id string) string { return prefix + id })
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
// tree needs: an empty anchor means home (the first plugin's root grid),
// exactly as the boot pane resolves it, and every leaf's grid fetch is
// kicked so the panes fill in. Loose per the urlwalk rule: a place that no
// longer resolves stays where its longest live prefix lands (gridIDForPane
// already walks loosely).
func (a *App) restoreWorkspaceLeaves(tree *pane.Tree) {
	tree.Walk(func(p *pane.Pane) {
		if p.Anchor == "" {
			p.Anchor = a.home
		}
		if p.Zoom == 0 {
			p.Zoom = 1
		}
		a.fetchGrid(a.gridIDForPane(p))
		if p.TextFocus != "" {
			a.autoLiveOnRestore(p.ID, p.TextFocus)
		}
	})
}

// ascendWorkspaceLevels leaves `count` workspaces: for each, flush the
// layout one last time, freeze and forget the inner leaves, pop, and
// restore the outer tree verbatim (focus returns to the origin pane). The
// FINAL landing animates the reverse of the descent — the pane tile's face
// shrinking back to the origin pane's viewport. A frame with no outer tree
// (boot restore) falls back to a fresh pane at the pane tile's containing
// grid — the same graceful degradation a portal ascent has after reload.
func (a *App) ascendWorkspaceLevels(count int) {
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
	// The restored outer leaves re-engage their descents (issue #202): the
	// boundary froze them; landing back is a re-entry, so the shell
	// reconnects and the url reopens — the same one-owner decision every
	// descent applies (restoreWorkspaceLeaves does this for the inward swap).
	a.tree.Walk(func(p *pane.Pane) {
		if p.TextFocus != "" {
			a.autoLiveOnRestore(p.ID, p.TextFocus)
		}
	})
	a.scheduleURLUpdate()
	a.draw()
}

// animateWorkspaceReturn plays the ascent's zoom-out: the origin pane starts
// zoomed into the pane tile's footprint (the reverse of the descent's end)
// and animates back to its restored viewport. Skipped — an instant landing —
// when the tile row isn't in the cached grid (nothing to zoom out of).
func (a *App) animateWorkspaceReturn(f workspace.Frame) {
	p := a.tree.FindPane(f.OriginPane)
	if p == nil || p.TextFocus != "" {
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
	if overtake < savedZoom {
		overtake = savedZoom
	}
	a.startTransition(&paneTransition{
		paneID: p.ID,
		segments: []transSegment{{
			path:   slices.Clone(p.Path),
			fromCx: tcx, fromCy: tcy, fromZoom: overtake,
			toCx: savedCx, toCy: savedCy, toZoom: savedZoom,
			durationMs: totalTransitionMs,
		}},
	})
}

// The bar itself — always-on, carrying the workspace crumbs AND the focused
// pane's descent chain — lives in bottombar.go (issue #212).

// commitWorkspaceRename posts the user-owned name for the workspace at
// `level` and updates its crumb + version claim from the response (a rename
// bumps the tile version — the layout persister's next write must carry the
// fresh claim rather than burn a conflict-retry).
func (a *App) commitWorkspaceRename(level int, alt string) {
	f := a.ws.At(level)
	if f == nil {
		return
	}
	tileID := f.TileID
	go func() {
		tile, err := a.postRename(tileID, alt)
		if err != nil {
			a.reportErr(errsurface.Error, "rename", "rename failed: "+rpcErrText(err))
			return
		}
		if fr := a.ws.At(level); fr != nil && fr.TileID == tileID {
			fr.Name = tile.AltText
			fr.TileVersion = tile.Version
		}
		a.c.UpdateTile(tile.GridID, *tile)
		a.draw()
	}()
}

// fallbackTreeFor builds the post-reload ascent landing: a fresh single pane
// that re-anchors to the pane tile's containing grid, viewport centered on
// the tile. The GetTile rides a goroutine — this runs inside a click
// callback, where a blocking network call would wedge the wasm scheduler —
// so the pane lands at home for a frame and re-anchors when the tile
// arrives; an unreachable tile just leaves it at home (best-effort, same as
// a portal ascent after reload).
func (a *App) fallbackTreeFor(tileID string) *pane.Tree {
	t := pane.NewTree()
	p := t.FocusedPane()
	p.Anchor = a.home
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
		if fp == nil || fp.Anchor != a.home || len(fp.Path) > 0 || fp.TextFocus != "" {
			return
		}
		fp.Anchor = tile.GridID
		fp.Cx = float64(tile.X) + float64(tile.W)/2
		fp.Cy = float64(tile.Y) + float64(tile.H)/2
		a.fetchGrid(tile.GridID)
		a.scheduleURLUpdate()
		a.draw()
	}()
	return t
}

// ── the persister ──────────────────────────────────────────────────────────

// scheduleWorkspaceSave arms the debounced layout persister. Called from
// draw() whenever the workspace stack is non-empty — the layout blob is
// DERIVED from the live tree by encode-and-diff, so there is no per-gesture
// persistence hook to forget (charter §1: a missed write is unrepresentable
// when there are no call sites).
func (a *App) scheduleWorkspaceSave() {
	if a.sched.wsSaveScheduled || a.ws.Depth() == 0 {
		return
	}
	a.sched.wsSaveScheduled = true
	js.Global().Call("setTimeout", a.sched.wsSaveCb, wsSaveDebounceMs)
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
		// One coalesced notice (same source key) — not one per save.
		a.reportErr(errsurface.Info, "layout:"+top.TileID,
			"a pane views content the workspace's node cannot reach; it will reopen at home")
	}
	if !workspace.ShouldPersist(top, data) {
		return
	}
	tileID, version := top.TileID, top.TileVersion
	go a.postPaneLayout(tileID, version, data)
}

// postPaneLayout sends one layout write (WriteContent — the one content
// door; a pane layout is framing-class and never bumps version), retrying once on a version
// conflict with a refetched claim (rename bumps the version; the layout
// write itself never does). Success marks the bytes saved and lets the
// response row fan into the cache exactly like an SSE event (one Apply
// owner). Failure surfaces (charter §6) and stays unsaved, so the next
// debounce tick retries naturally.
func (a *App) postPaneLayout(tileID string, version int64, data []byte) {
	tile, err := a.cl.WriteContent(context.Background(), tileID, version, data)
	if err != nil && isVersionConflict(err) {
		if fresh, gerr := a.cl.GetTile(context.Background(), tileID); gerr == nil {
			if top := a.ws.Top(); top != nil && top.TileID == tileID {
				top.TileVersion = fresh.Version
			}
			tile, err = a.cl.WriteContent(context.Background(), tileID, fresh.Version, data)
		}
	}
	if err != nil {
		a.surfaceRPCError("WriteContent", err)
		return
	}
	if top := a.ws.Top(); top != nil && top.TileID == tileID {
		workspace.MarkSaved(top, data)
		top.TileVersion = tile.Version
	}
	a.c.Apply(rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: *tile}})
	a.resolveErr("rpc:WriteContent")
}
