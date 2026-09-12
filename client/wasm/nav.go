//go:build js && wasm

package main

// Navigation: one descent and one ascent. The decisions are client/nav's, a
// gesture plus a world snapshot in and an ordered effect list out. This file
// is the gathering half and nav_exec.go the executing half; nothing here
// decides anything. Ownership boundaries are wire declarations on the doorway
// tile, which the machine reads, so no call site switches on a kind.

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/door"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/nav"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/pluginhealth"
)

// navGestureSteps turns a machine bug into a notice instead of a hang. The
// loop terminates anyway, since a plan re-plans only after consuming
// something.
const navGestureSteps = 64

// runGesture is the one entry into navigation: gather, plan, run, repeat
// while the machine hands back a continuation. The re-gather keeps a step
// honest that reads state the effects above it changed.
func (a *App) runGesture(g nav.Gesture) {
	for i := 0; i < navGestureSteps; i++ {
		plan := a.nav.Do(g, a.navWorld(g))
		a.runNav(plan)
		if plan.Next == nil {
			return
		}
		g = *plan.Next
	}
	a.reportErr(errsurface.Error, "nav", "navigation did not settle")
}

// descend takes pane p through the doorway tile: the descent verb.
func (a *App) descend(p *pane.Pane, tile *gridwellv1.Tile) {
	a.runGesture(nav.Gesture{Kind: nav.GestureDescend, PaneID: p.ID, Door: tile})
}

// ascend leaves n levels of pane p's place. animate asks for the zoom-out
// onto the doorway on the last hop; false is for the paths with no footprint
// to zoom out of.
func (a *App) ascend(p *pane.Pane, n int, animate bool) {
	a.runGesture(nav.Gesture{Kind: nav.GestureAscend, PaneID: p.ID, N: n, Animate: animate})
}

// ascendPane is ascend(1), named for what the gesture means: the middle
// button, and the bar's slot.
func (a *App) ascendPane(p *pane.Pane) {
	a.ascend(p, 1, true)
}

// navReEngage re-applies the auto-live verdict to a pane sitting in a content
// descent: the restore paths' arm of the one go-live owner.
func (a *App) navReEngage(paneID, tileID string) {
	a.runGesture(nav.Gesture{Kind: nav.GestureReEngage, PaneID: paneID, TileID: tileID})
}

// navWorld resolves the snapshot a gesture is planned against.
func (a *App) navWorld(g nav.Gesture) nav.World {
	w := a.navWorldCommon()
	switch g.Kind {
	case nav.GestureDescend:
		w.Door = a.navWorldForDescend(g.Door)
	case nav.GestureAscend:
		w.Leave = a.navWorldForAscend(g.PaneID)
	case nav.GestureRestore:
		w.Restore = a.navWorldForRestore().Restore
	case nav.GestureLandLevel:
		w.Level = a.navWorldForLevel(g.PaneID, g.TileID)
	case nav.GesturePromote:
		w.Promote = &nav.PromoteWorld{OldTile: a.cachedTileByID(g.OldID)}
	}
	return w
}

// navWorldForLevel resolves the pane tile's row as the landing pane's grid
// holds it. A grid that was never cached makes the landing instant.
func (a *App) navWorldForLevel(paneID, tileID string) *nav.LevelWorld {
	lw := &nav.LevelWorld{}
	p := a.tree.FindPane(paneID)
	if p == nil {
		return lw
	}
	g, ok := a.c.Grid(a.gridIDForPane(p))
	if !ok {
		return lw
	}
	if t, ok := g.Tiles[tileID]; ok {
		lw.Tile = t
	}
	return lw
}

// navWorldForRestore is the snapshot a restore and every step of its walk are
// planned against. The cached set is projected whole, because which grids a
// path reaches is what the walk decides.
func (a *App) navWorldForRestore() nav.World {
	w := a.navWorldCommon()
	rw := &nav.RestoreWorld{
		Grids:     map[string]map[string]nav.RestoreTile{},
		Failed:    map[string]bool{},
		RootViews: map[string]nav.Viewport{},
	}
	for _, gid := range a.c.KnownGridIDs() {
		g, ok := a.c.Grid(gid)
		if !ok {
			continue
		}
		rows := make(map[string]nav.RestoreTile, len(g.Tiles))
		for id, t := range g.Tiles {
			rows[id] = nav.RestoreTile{
				ChildGridID:  t.ChildGridId,
				IsWell:       rpc.IsWellKind(t.Kind),
				IsContent:    rpc.IsContentDescentKind(t.Kind),
				TextDocument: rpc.TextDocument(t),
				ReadOnly:     a.tileReadOnly(t),
				TextY:        t.TextY,
				TextMode:     t.TextMode,
			}
		}
		rw.Grids[gid] = rows
	}
	for _, id := range a.fetch.gridLoadFailed.Keys() {
		rw.Failed[id] = true
	}
	// Which doorway the address names is the machine's to decode, so every
	// one is resolved. door.Places is ByRoot's answer set.
	if p := a.tree.FocusedPane(); p != nil {
		for _, pd := range door.Places(a.allPlugins()) {
			if cx, cy, zoom, ok := a.persistedGridView(p, pd.Plugin.RootGridId, nil); ok {
				rw.RootViews[pd.Plugin.RootGridId] = nav.Viewport{Cx: cx, Cy: cy, Zoom: zoom}
			}
		}
	}
	w.Restore = rw
	return w
}

// navWorldCommon resolves the half of the snapshot every verb reads.
func (a *App) navWorldCommon() nav.World {
	w := nav.World{
		Focus:           a.tree.Focus,
		Home:            a.home,
		CellPx:          cellPx,
		TransitionMs:    totalTransitionMs,
		ZoomDistFactor:  zoomDistFactor,
		TextSideInset:   textSideInset,
		Animating:       map[string]bool{},
		MenuOpenOn:      a.menu.PaneID(),
		Caps:            a.caps,
		Surfaces:        append(a.urlSurfaces(), a.shellSurfaces()...),
		LevelDepth:      a.ws.Depth(),
		LevelTop:        a.ws.Top(),
		ShellAlive:      map[string]bool{},
		ShellAliveKnown: map[string]bool{},
	}
	rects := a.layoutPanes()
	a.tree.Walk(func(p *pane.Pane) {
		r, onScreen := rects[p.ID]
		// One walk of the place per pane: the grid and its scratch stamp are
		// the same read.
		gid := a.gridIDForPane(p)
		w.Panes = append(w.Panes, nav.PaneView{
			ID:          p.ID,
			Stack:       p.Stack.Clone(),
			Cx:          p.Cx,
			Cy:          p.Cy,
			Zoom:        p.Zoom,
			TextScrollX: p.TextScrollX,
			TextScrollY: p.TextScrollY,
			TextMode:    p.TextMode,
			Rect:        r,
			OnScreen:    onScreen,
			GridID:      gid,
			Scratch:     a.scratchGridIn(gid),
		})
		w.Animating[p.ID] = a.trans.Active(p.ID)
	})
	// A missing key means unknown, which is not dead, so the two maps keep
	// that distinction across the seam.
	for id, alive := range a.shellAlive {
		w.ShellAlive[id] = alive
		w.ShellAliveKnown[id] = true
	}
	return w
}

// navWorldForDescend resolves the doorway declarations only the shim can
// read, each through the predicate that owns it.
func (a *App) navWorldForDescend(tile *gridwellv1.Tile) *nav.DoorWorld {
	d := &nav.DoorWorld{
		DeadLink: a.deadLink(tile),
		IsLink:   isLinkTile(tile),
		ReadOnly: a.tileReadOnly(tile),
	}
	if tile.ChildGridId != "" {
		_, d.ChildGridCached = a.c.Grid(tile.ChildGridId)
		return d
	}
	if !rpc.IsWellKind(tile.Kind) {
		return d
	}
	// A doorway with no target: ask pluginhealth why, so the click says
	// something. The id is bare on a menu row and node-qualified on a link
	// tile, so both shapes are tried.
	pl, ok := a.pluginByUUID(tile.Id)
	if !ok {
		pl, ok = a.pluginByUUID(rpc.LocalOf(tile.Id))
	}
	if !ok {
		return d
	}
	if sev, source, message, ok := pluginhealth.ClickNotice(pl); ok {
		d.Health = &nav.Notice{Severity: sev, Source: source, Message: message}
	}
	return d
}

// navWorldForAscend resolves the frame being left: the content row, the
// doorway row one level out, and the framing the landing grid was left at.
func (a *App) navWorldForAscend(paneID string) *nav.LeaveWorld {
	lw := &nav.LeaveWorld{}
	p := a.tree.FindPane(paneID)
	if p == nil {
		return lw
	}
	own := p.FramingTarget()
	switch {
	case own.Content:
		if file, ok := a.descendedTile(p); ok {
			lw.DescendedTile = file
		}
	case own.TileID != "":
		lw.DoorGridID = a.gridIDForPathFrom(own.DoorAnchor, own.DoorPath)
		if g, ok := a.c.Grid(lw.DoorGridID); ok {
			lw.DoorGridCached = true
			if t, ok := g.Tiles[own.TileID]; ok {
				lw.DoorTile = t
			}
		}
	}
	// A frame restored from a URL or a layout blob carries no viewport, so
	// the ascent lands at the grid's persisted framing.
	landing := p.Popped(1)
	if !landing.HasView() && !landing.Content {
		if cx, cy, zoom, ok := a.persistedGridView(p, landing.Anchor(), landing.Path()); ok {
			lw.LandingView = &nav.Viewport{Cx: cx, Cy: cy, Zoom: zoom}
		}
	}
	return lw
}

// urlSurfaces and shellSurfaces list the panes holding a live surface: the
// input to pane.TakeOver, read straight off the live handles so nothing is
// mirrored.
func (a *App) urlSurfaces() []pane.Holder {
	var out []pane.Holder
	for id, pl := range a.locals {
		if pl.urlView != nil {
			out = append(out, pane.Holder{PaneID: id, TileID: pl.urlView.tileID})
		}
	}
	return out
}

func (a *App) shellSurfaces() []pane.Holder {
	var out []pane.Holder
	for id, pl := range a.locals {
		if pl.shellConn != nil {
			out = append(out, pane.Holder{PaneID: id, TileID: pl.shellConn.tileID})
		}
	}
	return out
}
