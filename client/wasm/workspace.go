//go:build js && wasm

package main

// The pane-tile level executor, and the layout persister.
//
// Descending into a pane tile swaps the whole pane tree — descend()'s window
// arm, the second axis beside a pane's own frame stack. What happens when, and
// in what order, is client/nav's (level.go): this file only performs the swap,
// the pop, and the capture, and keeps the layout blob current with a debounced
// snapshot diff. The rules live in pure packages: client/pane's Levels holds
// the level stack and the persist decision, client/wsbar the bar geometry.
//
// Bar gestures: a left-click on a level crumb leaves level k and everything
// deeper; a right-click renames it inline. Descent and ascent animate like
// every other tile: the zoom rides through the pane tile's footprint, and
// because the preview is the live layout under one uniform scale
// (client/panepreview's tested property), the swap lands on exactly what the
// preview showed.

import (
	"context"
	"strings"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/nav"
	"github.com/josephburnett/gridwell/client/pane"
)

// wsSaveDebounceMs is the persister's coalescing window: every
// layout-affecting gesture inside a pane tile lands in the blob at most this
// long after it settles, plus the flush on ascent. A reload inside the window
// loses at most this much arrangement.
const wsSaveDebounceMs = 500

// wsExpandState is the first-descent capture animation: the pane tile's
// screen rect at arm, growing into the level outline while the content
// underneath never moves. Drawn at the end of draw() for exactly as long as
// the machine has a level descent pending, so the install — where the real
// outline takes over seamlessly — and a failed descent both end it without
// anyone remembering to.
type wsExpandState struct {
	x, y, w, h float64
	startMs    float64
}

// installLevel performs the swap: push the level and install its tree, with
// the outer level left running — its live views park off-screen and its shells
// stay attached, because liveness follows pane existence and no pane closed
// here. Level-scoped pane ids (Tree.IDPrefix) keep the simultaneously-alive
// trees from colliding in the pane-keyed maps.
//
// KeepOuter=false means the descent has no return tree (a boot restore through
// ?w=, whose boot-blank tree is nothing the user built): the level records
// OuterTree nil and ascent falls back to the pane tile's containing grid.
// Capture means the tree to install is the window layout as it stands right
// now — the first descent into a never-arranged tile, so you keep looking at
// exactly what you had, now inside the pane tile.
func (a *App) navInstallLevel(e nav.Effect) {
	if e.Level == nil {
		return
	}
	f := *e.Level
	if e.KeepOuter {
		f.OuterTree = a.tree
	}
	tree := e.Tree
	if tree == nil && e.Capture {
		tree = a.captureWorkspaceTree(e.TileID, e.IDPrefix, e.PaneID)
	}
	if tree == nil {
		return
	}
	// baseline is the decoded blob bytes, nil for a never-arranged tile,
	// seeding the persister's diff so a pure visit never writes.
	pane.MarkSaved(&f, e.Baseline)
	a.ws.Push(f)
	a.tree = tree
	a.restoreWorkspaceLeaves(tree)
}

// navPopLevel leaves one level: the parked tree comes back verbatim with focus
// on the origin pane, or — for a level that parked none — a fresh single pane
// at the grid the plan named.
func (a *App) navPopLevel(e nav.Effect) {
	f, ok := a.ws.Pop()
	if !ok {
		return
	}
	if f.OuterTree != nil {
		a.tree = f.OuterTree
		if f.OriginPane != "" && a.tree.FindPane(f.OriginPane) != nil {
			a.tree.Focus = f.OriginPane
		}
		return
	}
	a.tree = pane.TreeAtPlace("", e.GridID, nil, 0, 0, 1)
}

// captureWorkspaceTree clones the current window layout as a fresh pane tile's
// initial arrangement: an encode and decode round trip through the same
// rel/abs pair the persister uses, so the capture is byte-for-byte what the
// first flush will store. One serialization owner, no second cloner. Any
// failure — a pane outside the node's reach encodes as home, per the flush
// rule, and a hard error falls all the way back — yields the single-pane
// default at the origin pane's place, because a capture must never block the
// descent.
func (a *App) captureWorkspaceTree(tileID, idPrefix, originPane string) *pane.Tree {
	prefix := pane.ChainPrefix(tileID)
	data, _, err := pane.EncodeLayout(a.tree, func(id string) (string, bool) {
		rest, ok := strings.CutPrefix(id, prefix)
		return rest, ok
	})
	if err == nil {
		if t, derr := pane.DecodeLayout(data, func(id string) string { return prefix + id }, idPrefix); derr == nil {
			// An ephemeral descent — a click-visit riding the scratch grid —
			// is session state that dies on ascent. A durable capture must not
			// reference it, and a copy going live would keep the outer visit's
			// view alive past the boundary. The captured pane keeps its place;
			// the visit stays with the outer tree and re-engages on ascent.
			t.Walk(func(cp *pane.Pane) {
				if cp.ContentID() == "" {
					return
				}
				if tile := a.findTileByID(cp.ContentID()); tile != nil && a.possiblyEphemeral(cp, tile) {
					cp.Pop()
				}
			})
			return t
		}
	}
	var origin pane.Stack
	if op := a.tree.FindPane(originPane); op != nil {
		origin = op.Stack
	}
	return pane.TreeAtPlace(idPrefix, origin.Anchor(), origin.Path(),
		origin.Cx, origin.Cy, origin.Zoom)
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
			a.navReEngage(p.ID, p.ContentID())
		}
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
		// The level may be gone by the time a parked retry lands, if the user
		// left it. The rename still landed on the tile row; only the crumb
		// update is conditional.
		if fr := a.ws.At(level); fr != nil && fr.TileID == tileID {
			fr.Name = tile.AltText
		}
		a.c.UpdateTile(tile.GridID, *tile)
	})
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
	prefix := pane.ChainPrefix(top.TileID)
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
