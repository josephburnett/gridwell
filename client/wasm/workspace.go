//go:build js && wasm

package main

// The pane-tile level executor and the layout persister. Descending into a
// pane tile swaps the whole pane tree, the second axis beside a pane's own
// frame stack. What happens when is client/nav's; this performs the swap, the
// pop and the capture, and keeps the layout blob current with a debounced
// snapshot diff.

import (
	"context"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"strings"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/nav"
	"github.com/josephburnett/gridwell/client/pane"
)

// wsSaveDebounceMs is the persister's coalescing window: a reload inside it
// loses at most that much arrangement.
const wsSaveDebounceMs = 500

// wsExpandState is the first-descent capture animation: the pane tile's rect
// at arm, growing into the level outline. Drawn for as long as the machine
// has a level descent pending, so nothing has to end it.
type wsExpandState struct {
	x, y, w, h float64
	startMs    float64
}

// navInstallLevel pushes the level and installs its tree, leaving the outer
// level running: liveness follows pane existence and no pane closed here.
// Level-scoped pane ids keep the alive trees from colliding in the pane-keyed
// maps. KeepOuter=false has no return tree, so ascent falls back to the pane
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
	a.restoreWorkspaceLeaves(tree)
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
		if f.OriginPane != "" && a.tree.FindPane(f.OriginPane) != nil {
			a.tree.Focus = f.OriginPane
		}
		return
	}
	a.tree = pane.TreeAtPlace("", e.GridID, nil, 0, 0, 1)
}

// captureWorkspaceTree clones the window layout as a fresh pane tile's
// initial arrangement, through the persister's own encode and decode pair, so
// the capture is byte-for-byte what the first flush stores. Any failure
// yields the single-pane default, because a capture must not block the
// descent, and says so: the arrangement the user was looking at is not the
// one they get.
func (a *App) captureWorkspaceTree(tileID, idPrefix, originPane string) *pane.Tree {
	prefix := pane.ChainPrefix(tileID)
	data, skipped, err := pane.EncodeLayout(a.tree, func(id string) (string, bool) {
		rest, ok := strings.CutPrefix(id, prefix)
		return rest, ok
	})
	a.reportLayoutSkipped(tileID, skipped)
	if err == nil {
		t, derr := pane.DecodeLayout(data, func(id string) string { return prefix + id }, idPrefix)
		if derr == nil {
			pane.PopEphemeralContent(t, func(cp *pane.Pane, contentID string) bool {
				// A tile this client has not cached cannot be asked about, so
				// the frame stays; the fetch findTileByID kicks answers later
				// visits.
				tile := a.findTileByID(contentID)
				return tile != nil && a.possiblyEphemeral(cp, tile)
			})
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
	return pane.TreeAtPlace(idPrefix, origin.Anchor(), origin.Path(),
		origin.Cx, origin.Cy, origin.Zoom)
}

// reportLayoutSkipped posts the one notice for panes an encode could not place
// in the owning node's frame, for the two callers that encode the live tree.
func (a *App) reportLayoutSkipped(tileID string, skipped []string) {
	if len(skipped) == 0 {
		return
	}
	// A pane looking outside the owning node's reach persists as home. One
	// coalesced notice on the source key, not one per save.
	a.reportErr(errsurface.Info, "layout:"+tileID,
		"a pane views content the workspace's node cannot reach; it will reopen at home")
}

// restoreWorkspaceLeaves applies the boot-blank fixups a freshly-installed
// tree needs: an empty anchor means the node's home grid, and every leaf's
// grid fetch is kicked. Loose, per the urlwalk rule.
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

// commitWorkspaceRename posts the user-owned name for the pane tile at
// `level`. A rename bumps the tile version, so the response updates the cache
// and the persister's next write carries the fresh claim.
func (a *App) commitWorkspaceRename(level int, alt string) {
	f := a.ws.At(level)
	if f == nil {
		return
	}
	tileID := f.TileID
	a.commitRenameRetained(tileID, alt, func(tile *gridwellv1.Tile) {
		// The level may be gone by the time a parked retry lands. The rename
		// still landed on the tile row; only the crumb update is
		// conditional.
		if fr := a.ws.At(level); fr != nil && fr.TileID == tileID {
			fr.Name = tile.AltText
		}
		a.c.UpdateTile(tile.GridId, tile)
	})
}

// ── the persister ──────────────────────────────────────────────────────────

// scheduleWorkspaceSave arms the debounced layout persister from draw(). The
// blob is derived from the live tree by encode and diff, so there is no
// per-gesture persistence hook to forget.
func (a *App) scheduleWorkspaceSave() {
	if a.ws.Depth() == 0 {
		return
	}
	a.persist.sched.wsSave.arm(wsSaveDebounceMs)
}

// flushWorkspaceSave persists the current layout if it changed: the
// ascent-boundary flush, and the debounce callback's body.
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
	a.reportLayoutSkipped(top.TileID, skipped)
	if !pane.ShouldPersist(top, data) {
		return
	}
	go a.postPaneLayout(top.TileID, data)
}

// postPaneLayout sends one layout write through WriteContent. A pane layout
// is framing-class: no version claim and no bump. A transport failure parks
// the encoded layout, because the ascent-boundary flush fires once and then
// pops the frame, leaving `data` the only copy.
func (a *App) postPaneLayout(tileID string, data []byte) {
	var tile *gridwellv1.Tile
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
			a.c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{
				TileChanged: &gridwellv1.TileChanged{Tile: tile}}})
			a.resolveErr("rpc:PaneLayout")
		},
		beacon: func() (string, []byte, string) {
			path, body := rpc.WriteContentBeacon(tileID, 0, data)
			return path, body, rpc.BeaconStreamType
		},
	})
}
