//go:build js && wasm

package main

// The workspace level executor and the layout persister. Descending into a
// workspace tile swaps the whole pane tree, the second axis beside a pane's own
// frame stack. What happens when is client/nav's; this performs the swap, the
// pop and the capture, and keeps the layout blob current with a debounced
// snapshot diff.

import (
	"context"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/client/cache"
	"github.com/josephburnett/gridwell/client/zoomtrans"
	"strings"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/cadence"
	"github.com/josephburnett/gridwell/client/clientsync"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/nav"
	"github.com/josephburnett/gridwell/client/outbox"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/traceevent"
)

// wsExpandState is the first-descent capture animation: the workspace tile's rect
// at arm, growing into the level outline. Drawn for as long as the machine
// has a level descent pending, so nothing has to end it.
type wsExpandState struct {
	x, y, w, h float64
	startMs    float64
}

// navInstallLevel pushes the level and installs its tree, leaving the outer
// level running: liveness follows pane existence and no pane closed here.
// Level-scoped pane ids keep the alive trees from colliding in the pane-keyed
// maps. KeepOuter=false has no return tree, so ascent falls back to the workspace
// tile's grid; Capture installs the window layout as it stands.
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
	// Seeds the persister's diff so a pure visit never writes.
	pane.MarkSaved(&f, e.Baseline)
	a.ws.Push(f)
	a.tree = tree
	a.restoreWorkspaceLeaves(tree, f.TileID)
}

// navReplaceLevel puts the arrangement nav.GestureReconcileLevel read in
// place of the open level's tree, seeding the persister's diff with it so the
// reconciled tree is not written back.
func (a *App) navReplaceLevel(e nav.Effect) {
	top := a.ws.Top()
	if top == nil || top.TileID != e.TileID || e.Level == nil {
		return
	}
	top.Basis, top.ReadOnly = e.Level.Basis, e.Level.ReadOnly
	if e.Tree == nil {
		return
	}
	pane.MarkSaved(top, e.Baseline)
	a.tree = e.Tree
	a.restoreWorkspaceLeaves(e.Tree, top.TileID)
}

// navPopLevel leaves one level: the parked tree comes back verbatim with
// focus on the origin pane, or a fresh single pane at the grid the plan
// named.
func (a *App) navPopLevel(e nav.Effect) {
	f, ok := a.ws.Pop()
	if !ok {
		return
	}
	if f.OuterTree != nil {
		a.tree = f.OuterTree
		if f.OriginPane != "" {
			_ = a.tree.SetFocus(f.OriginPane)
		}
		return
	}
	a.tree = pane.TreeAtPlace("", e.GridID, nil, rpc.View{})
}

// captureWorkspaceTree copies the window layout as a fresh workspace tile's
// initial arrangement, ephemeral visits included, through the persister's own
// encode and decode pair, so the capture is byte-for-byte what the first flush
// stores. Any failure
// yields the single-pane default, because a capture must not block the
// descent, and says so: the arrangement the user was looking at is not the
// one they get.
func (a *App) captureWorkspaceTree(tileID, idPrefix, originPane string) *pane.Tree {
	data, skipped, err := pane.EncodeLayout(a.tree, pane.OwnerFrame(tileID))
	a.reportLayoutSkipped(tileID, skipped)
	if err == nil {
		prefix := pane.ChainPrefix(tileID)
		t, derr := pane.DecodeLayout(data, func(id string) string { return prefix + id }, idPrefix)
		if derr == nil {
			return t
		}
		err = derr
	}
	a.reportErr(errsurface.Error, "layout:"+tileID,
		"workspace layout capture failed — opened with one pane: "+err.Error())
	var origin pane.Stack
	if op := a.tree.FindPane(originPane); op != nil {
		origin = op.Stack
	}
	return pane.TreeAtPlace(idPrefix, origin.Anchor(), origin.Path(), origin.View)
}

// reportLayoutSkipped posts the one notice for an encode of the live tree,
// by either caller, that had a pane its owning node cannot name at all
// (pane.Stack.Respelled), coalesced on the source key.
func (a *App) reportLayoutSkipped(tileID string, skipped []string) {
	if len(skipped) == 0 {
		return
	}
	a.reportErr(errsurface.Info, "layout:"+tileID,
		"a pane shows something this workspace's node cannot reach, so the workspace keeps it at that node's home")
}

// restoreWorkspaceLeaves places a freshly-installed tree's empty anchors at
// the home of the node owning workspace tile tileID, from its handshake, so a
// pane stays blank until that answers and fetchMenuCtx calls this again. It
// reports whether it placed one; the leaves' reads are nav.GestureEngageLeaves.
func (a *App) restoreWorkspaceLeaves(tree *pane.Tree, tileID string) bool {
	home := a.nodeCtx(strings.TrimSuffix(pane.ChainPrefix(tileID), "/")).home
	placed := false
	tree.Walk(func(p *pane.Pane) {
		if p.Anchor() == "" && home != "" {
			p.Reset(pane.Frame{GridID: home, View: rpc.Saved(zoomtrans.Origin), ViewPending: true})
			placed = true
		}
	})
	return placed
}

// commitWorkspaceRename posts the user-owned name for the workspace tile at
// `level`, claiming cl, opened on the level's basis. A rename bumps the tile
// version, so it rides the layout's save chain, a layout landing moves cl
// (postPaneLayout), and its own landing advances the level's basis like a
// layout write's.
func (a *App) commitWorkspaceRename(level int, cl *outbox.Claim, alt string) {
	f := a.ws.At(level)
	if f == nil {
		a.persist.out.Claims.Close(cl)
		return
	}
	tileID := f.TileID
	a.commitRename(cl, tileID, alt, func(tile *gridwellv1.Tile, claimed int64) {
		a.ws.Landed(tileID, claimed, tile.Version)
		// The level may be gone by the time a parked retry lands. The rename
		// still landed on the tile row; only the crumb update is
		// conditional.
		if fr := a.ws.At(level); fr != nil && fr.TileID == tileID {
			fr.Name = tile.AltText
		}
	})
}

// ── the persister ──────────────────────────────────────────────────────────

// scheduleWorkspaceSave arms the debounced layout persister from draw(), keyed
// on the tree the blob is encoded from. The blob is derived by encode and
// diff, so there is no per-gesture persistence hook to forget.
func (a *App) scheduleWorkspaceSave(fp pane.Fingerprint) {
	if a.ws.Depth() == 0 {
		return
	}
	a.persist.sched.wsSave.ArmOnChange(cadence.WorkspaceSaveMs, fp.Value())
}

// flushWorkspaceSave persists the current layout if it changed: the
// ascent-boundary flush, the debounce callback's body, and
// nav.RequestFlushLayout, whose held hears whether the node holds the tree as
// it stands. held may be nil.
func (a *App) flushWorkspaceSave(held func(ok bool)) {
	if held == nil {
		held = func(bool) {}
	}
	top := a.ws.Top()
	if top == nil {
		go held(true)
		return
	}
	data, skipped, err := pane.EncodeLayout(a.tree, pane.OwnerFrame(top.TileID))
	if err != nil {
		a.reportErr(errsurface.Error, "layout:"+top.TileID, "workspace layout encode failed: "+err.Error())
		go held(false)
		return
	}
	a.reportLayoutSkipped(top.TileID, skipped)
	if !pane.ShouldPersist(top, data) {
		// Nothing new, but the node holds the tree only once the write
		// still on the chain has landed.
		a.persist.contentSaves.After(top.TileID, held)
		return
	}
	pane.MarkSaved(top, data)
	a.postPaneLayout(top.TileID, data, held)
}

// postPaneLayout sends one layout write through WriteContent on the tile's
// save chain, so no older layout lands after it (see outbox.SaveQueue). A
// layout is the user's bytes: it claims the version its tree derives from
// (pane.Levels.Claim) and bumps it. A transport failure parks the encoded
// layout, because the ascent-boundary flush fires once and then pops the
// frame, leaving `data` the only copy; a conflict hands the level to
// nav.GestureReconcileLevel. held hears the first attempt's verdict: a parked
// write is not held.
func (a *App) postPaneLayout(tileID string, data []byte, held func(ok bool)) {
	var tile *gridwellv1.Tile
	var claimed int64
	var callErr error
	fallback := a.ws.Claim(tileID, 0)
	landed, answered := false, false
	end := func() {}
	a.post(write{
		label: "PaneLayout", gid: a.gridIDOfTile(tileID), id: tileID, chain: tileID,
		source: "layout:" + tileID, failText: "workspace layout unsaved",
		call: func(ctx context.Context) error {
			end = a.c.BeginWrite(tileID)
			claimed = a.ws.Claim(tileID, fallback)
			tile, callErr = a.cl.WriteContent(ctx, tileID, rpc.ContentBasis{Version: claimed}, data)
			return callErr
		},
		then: func() {
			a.ws.Landed(tileID, claimed, tile.Version)
			a.persist.out.Claims.Landed(tileID, claimed, tile.Version)
			a.c.PutSavedContent(tile, data)
			a.c.PutWriteResponse(tile.GridId, tile, cache.WroteBody)
			a.resolveErr("rpc:PaneLayout")
			landed = true
			// A draw of the echo skipped the body while it was in flight.
			a.scheduleDraw(traceevent.WhyContent)
		},
		done: func() {
			end()
			conflict := !landed && clientsync.Of(callErr) == clientsync.OutcomeConflict
			if conflict {
				// The arrangement on disk wins; the refused bytes are not
				// sent again.
				a.runGesture(nav.Gesture{Kind: nav.GestureReconcileLevel, TileID: tileID})
			}
			if answered {
				return
			}
			answered = true
			if !landed && !conflict {
				if top := a.ws.Top(); top != nil && top.TileID == tileID {
					pane.ForgetSaved(top, data)
				}
			}
			held(landed)
		},
		beacon: func() (string, []byte, string) {
			claim := rpc.ContentBasis{Version: a.ws.Claim(tileID, fallback)}
			path, body := rpc.WriteContentBeacon(tileID, claim, data)
			return path, body, rpc.BeaconStreamType
		},
	})
}
