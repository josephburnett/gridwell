//go:build js && wasm

package main

// Workspace navigation: descending into a pane tile swaps the whole pane
// tree (the THIRD descent verb — neither a path push nor a portal anchor
// swap), the bottom bar names the nesting and owns the way back out, and a
// debounced snapshot-diff persister keeps the layout blob current while
// inside. The rules live in pure packages — client/workspace (the stack and
// the write decision) and client/wsbar (the bar geometry) — this file is
// the glue: gestures in, RPCs out, draw calls between.

import (
	"context"
	"strings"
	"syscall/js"

	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/workspace"
	"github.com/josephburnett/gridwell/client/wsbar"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// wsSaveDebounceMs is the persister's coalescing window: every layout-
// affecting gesture inside a workspace lands in the blob at most this long
// after it settles (plus the flush on ascent). A reload inside the window
// loses at most this much arrangement.
const wsSaveDebounceMs = 500

// startWorkspaceDescent enters a pane tile from pane p. The layout bytes may
// not be cached yet; the install happens when they are (usually the same
// tick — the preview already fetched them). A blob that cannot be decoded
// installs the default single pane READ-ONLY: the session must never
// overwrite a blob it could not read (a newer format downgraded would be
// rewriting history).
func (a *App) startWorkspaceDescent(p *pane.Pane, pt *rpc.Tile) {
	originPane := p.ID
	tileID := pt.ID
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
			return
		}
		if fresh.BlobID == 0 {
			// Never arranged: the default workspace, writable from the start.
			a.installWorkspace(fresh, defaultWorkspaceTree(), originPane, false, nil, true)
			return
		}
		data, err := a.cl.GetTileContent(context.Background(), tileID)
		if err != nil {
			a.surfaceRPCError("GetTileContent", err)
			return
		}
		prefix := paneTileChainPrefix(tileID)
		tree, derr := pane.DecodeLayout(data, func(id string) string { return prefix + id })
		readOnly := false
		if derr != nil {
			a.reportErr(errsurface.Error, "layout:"+tileID,
				"workspace layout unreadable — opened read-only: "+derr.Error())
			tree = defaultWorkspaceTree()
			readOnly = true
		}
		a.installWorkspace(fresh, tree, originPane, readOnly, data, true)
	}()
}

// defaultWorkspaceTree is a fresh workspace's single pane at home (like a
// fresh boot). The bootstrap anchor is applied by installWorkspace via the
// same home the boot pane uses.
func defaultWorkspaceTree() *pane.Tree {
	return pane.NewTree()
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
		Name:        workspaceCrumbName(pt),
		ReadOnly:    readOnly,
	}
	workspace.MarkSaved(&f, baseline)
	a.ws.Push(f)

	a.tree = tree
	a.restoreWorkspaceLeaves(tree)
	a.scheduleURLUpdate()
	a.draw()
}

// bootWorkspace restores the innermost workspace from a reload (?w=). The
// outer tree is nil by design — nesting membership is session-only, like
// portal Up frames.
func (a *App) bootWorkspace(tileID string) {
	tile, err := a.cl.GetTile(context.Background(), tileID)
	if err != nil {
		a.surfaceRPCError("GetTile", err)
		return
	}
	if !rpc.IsWorkspaceKind(tile.Kind) {
		a.reportErr(errsurface.Error, "layout:"+tileID, "?w= names a non-workspace tile")
		return
	}
	if tile.BlobID == 0 {
		a.installWorkspace(tile, defaultWorkspaceTree(), "", false, nil, false)
		return
	}
	data, err := a.cl.GetTileContent(context.Background(), tileID)
	if err != nil {
		a.surfaceRPCError("GetTileContent", err)
		return
	}
	prefix := paneTileChainPrefix(tileID)
	tree, derr := pane.DecodeLayout(data, func(id string) string { return prefix + id })
	readOnly := false
	if derr != nil {
		a.reportErr(errsurface.Error, "layout:"+tileID,
			"workspace layout unreadable — opened read-only: "+derr.Error())
		tree = defaultWorkspaceTree()
		readOnly = true
	}
	a.installWorkspace(tile, tree, "", readOnly, data, false)
}

// restoreWorkspaceLeaves applies the boot-blank fixups a freshly-installed
// tree needs: an empty anchor means home (the node grid), exactly as the
// boot pane resolves it, and every leaf's grid fetch is kicked so the panes
// fill in. Loose per the urlwalk rule: a place that no longer resolves stays
// where its longest live prefix lands (gridIDForPane already walks loosely).
func (a *App) restoreWorkspaceLeaves(tree *pane.Tree) {
	tree.Walk(func(p *pane.Pane) {
		if p.Anchor == "" {
			p.Anchor = a.nodeGrid
		}
		if p.Zoom == 0 {
			p.Zoom = 1
		}
		a.fetchGrid(a.gridIDForPane(p))
	})
}

// workspaceCrumbName is the bar label for a pane tile: its alt (the
// workspace name) or the kind's generic label when unnamed.
func workspaceCrumbName(pt *rpc.Tile) string {
	if pt.AltText != "" {
		return pt.AltText
	}
	return "workspace"
}

// ascendWorkspaceLevels leaves `count` workspaces: for each, flush the
// layout one last time, freeze and forget the inner leaves, pop, and
// restore the outer tree verbatim (focus returns to the origin pane). A
// frame with no outer tree (boot restore) falls back to a fresh pane at the
// pane tile's containing grid — the same graceful degradation a portal
// ascent has after reload.
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
		} else {
			a.tree = a.fallbackTreeFor(f.TileID)
		}
	}
	a.scheduleURLUpdate()
	a.draw()
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
	p.Anchor = a.nodeGrid
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
		if fp == nil || fp.Anchor != a.nodeGrid || len(fp.Path) > 0 || fp.TextFocus != "" {
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

// ── the bar ────────────────────────────────────────────────────────────────

// workspaceBarTop returns the bar band's top edge: the bar sits directly
// above the notice strip (which keeps the very bottom).
func (a *App) workspaceBarTop() float64 {
	return a.height - errsurface.StripHeight(a.errs.Len()) - wsbar.Height(a.ws.Depth())
}

// drawWorkspaceBar paints the breadcrumb band. Geometry comes from wsbar so
// the click hit-test (workspaceBarClick) reads the identical layout — render
// and input cannot disagree.
func (a *App) drawWorkspaceBar() {
	depth := a.ws.Depth()
	if depth == 0 {
		return
	}
	c := a.cctx
	top := a.workspaceBarTop()
	c.Set("fillStyle", colorPaneTileFill)
	c.Call("fillRect", 0, top, a.width, wsbar.RowH)
	names := a.ws.Names()
	segs := wsbar.Segments(len(names), a.width)
	c.Set("font", "12px system-ui, sans-serif")
	c.Set("textBaseline", "middle")
	for i, s := range segs {
		// Crumb face: the current (rightmost) workspace reads brightest.
		if s.Level == depth {
			c.Set("fillStyle", colorPaneTileBorder)
		} else {
			c.Set("fillStyle", "#1d4a4a")
		}
		c.Call("fillRect", s.X+2, top+3, s.W-4, wsbar.RowH-6)
		c.Set("fillStyle", "#dff4f4")
		label := names[i]
		if label == "" {
			label = "workspace"
		}
		c.Call("save")
		c.Call("beginPath")
		c.Call("rect", s.X+2, top, s.W-4, wsbar.RowH)
		c.Call("clip")
		c.Call("fillText", label, s.X+10, top+wsbar.RowH/2)
		c.Call("restore")
	}
	c.Call("fillRect", 0, top, a.width, 1) // hairline above the band
}

// workspaceBarClick consumes a left-click in the bar band: clicking crumb k
// LEAVES workspace k and everything deeper (the rightmost crumb leaves just
// the current one). Returns true when the click was in the band, whether or
// not it hit a crumb, so the click never falls through to a pane below.
func (a *App) workspaceBarClick(sx, sy float64) bool {
	depth := a.ws.Depth()
	if depth == 0 {
		return false
	}
	top := a.workspaceBarTop()
	if sy < top || sy >= top+wsbar.RowH {
		return false
	}
	segs := wsbar.Segments(depth, a.width)
	if level := wsbar.SegmentAt(segs, sx); level > 0 {
		a.ascendWorkspaceLevels(a.ws.PopCountForCrumb(level))
	}
	return true
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

// postPaneLayout sends one SetPaneLayout, retrying once on a version
// conflict with a refetched claim (rename bumps the version; the layout
// write itself never does). Success marks the bytes saved and lets the
// response row fan into the cache exactly like an SSE event (one Apply
// owner). Failure surfaces (charter §6) and stays unsaved, so the next
// debounce tick retries naturally.
func (a *App) postPaneLayout(tileID string, version int64, data []byte) {
	tile, err := a.cl.SetPaneLayout(context.Background(), tileID, version, data)
	if err != nil && isVersionConflict(err) {
		if fresh, gerr := a.cl.GetTile(context.Background(), tileID); gerr == nil {
			if top := a.ws.Top(); top != nil && top.TileID == tileID {
				top.TileVersion = fresh.Version
			}
			tile, err = a.cl.SetPaneLayout(context.Background(), tileID, fresh.Version, data)
		}
	}
	if err != nil {
		a.surfaceRPCError("SetPaneLayout", err)
		return
	}
	if top := a.ws.Top(); top != nil && top.TileID == tileID {
		workspace.MarkSaved(top, data)
		top.TileVersion = tile.Version
	}
	a.c.Apply(rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: *tile}})
	a.resolveErr("rpc:SetPaneLayout")
}
