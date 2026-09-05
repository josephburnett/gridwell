//go:build js && wasm

package main

// Navigation: one descent and one ascent.
//
// Physically there is one gesture — go through a doorway, or come back out
// the way you came in. The decisions are client/nav's: a gesture plus a world
// snapshot in, an ordered effect list out. This file is the gathering half —
// every impure read the machine needs, resolved up front — and nav_exec.go is
// the executing half. Nothing here decides anything.
//
// The data model's ownership boundaries — a well, a link into another
// namespace, a content tile — are wire declarations on the doorway tile. The
// machine reads them; no call site switches on a kind.

import (
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/nav"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/pluginhealth"
)

// navGestureSteps bounds the gather-plan-execute loop. A plan asks to be
// re-planned only after it has consumed something — a running transition
// landed, one ascent hop popped — so the loop always terminates; the bound is
// the backstop that turns a machine bug into a notice instead of a hang.
const navGestureSteps = 64

// runGesture is the one entry into navigation: gather the world, plan the
// gesture against it, run the effects, and repeat while the machine hands
// back a continuation gesture. The re-gather is what keeps a step whose
// successor reads state the effects above it just changed honest — a descent
// that first lands a running transition, and each hop of a multi-level
// ascent.
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
func (a *App) descend(p *pane.Pane, tile *rpc.Tile) {
	a.runGesture(nav.Gesture{Kind: nav.GestureDescend, PaneID: p.ID, Door: *tile})
}

// ascend leaves n levels of pane p's place: the ascent verb, for one level or
// several. animate asks for the zoom-out onto the doorway on the last hop;
// false makes even that instant, for the paths with no footprint to zoom out
// of.
func (a *App) ascend(p *pane.Pane, n int, animate bool) {
	a.runGesture(nav.Gesture{Kind: nav.GestureAscend, PaneID: p.ID, N: n, Animate: animate})
}

// ascendPane is the one-level ascent gesture: the middle button, and the
// bar's slot. It is ascend(1), named for what the gesture means.
func (a *App) ascendPane(p *pane.Pane) {
	a.ascend(p, 1, true)
}

// navReEngage re-applies the auto-live verdict to a pane that is sitting in a
// content descent: the restore paths' arm of the one go-live owner. The
// machine re-checks that the pane is still in this descent, since the row it
// carries was read asynchronously.
func (a *App) navReEngage(paneID string, tile *rpc.Tile) {
	a.runGesture(nav.Gesture{Kind: nav.GestureReEngage, PaneID: paneID, Door: *tile})
}

// navWorld resolves the snapshot a gesture is planned against: the common
// half every verb reads, plus the verb's own.
func (a *App) navWorld(g nav.Gesture) nav.World {
	w := a.navWorldCommon()
	switch g.Kind {
	case nav.GestureDescend, nav.GestureReEngage:
		w.Door = a.navWorldForDescend(g.PaneID, &g.Door)
	case nav.GestureAscend:
		w.Leave = a.navWorldForAscend(g.PaneID)
	}
	return w
}

// navWorldCommon resolves the half of the snapshot every verb reads: where
// each pane is, what it is animating, what the window can do.
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
			GridID:      a.gridIDForPane(p),
		})
		w.Animating[p.ID] = a.trans.Active(p.ID)
	})
	// A missing key means unknown, which is not dead: the two maps keep that
	// distinction across the seam.
	for id, alive := range a.shellAlive {
		w.ShellAlive[id] = alive
		w.ShellAliveKnown[id] = true
	}
	return w
}

// navWorldForDescend resolves the doorway half: the declarations only the
// shim can read, each through the predicate that owns it.
func (a *App) navWorldForDescend(paneID string, tile *rpc.Tile) *nav.DoorWorld {
	d := &nav.DoorWorld{
		DeadLink: a.deadLink(tile),
		IsLink:   isLinkTile(tile),
		ReadOnly: a.tileReadOnly(tile),
	}
	if p := a.tree.FindPane(paneID); p != nil {
		d.PaneScratch = a.scratchGridOf(p)
	}
	if tile.ChildGridID != "" {
		_, d.ChildGridCached = a.c.Grid(tile.ChildGridID)
		return d
	}
	if !rpc.IsWellKind(tile.Kind) {
		return d
	}
	// A doorway with no target: ask pluginhealth why, so the click says
	// something instead of silently doing nothing. The id is the plugin uuid
	// itself on a menu row (chained for a connection, "i9sm6ff/ltvv2f9") and
	// node-qualified on a link tile, so try both shapes or a connection's
	// dial status never surfaces.
	pl, ok := a.pluginByUUID(tile.ID)
	if !ok {
		pl, ok = a.pluginByUUID(rpc.LocalOf(tile.ID))
	}
	if !ok {
		return d
	}
	if sev, source, message, ok := pluginhealth.ClickNotice(pl); ok {
		d.Health = &nav.Notice{Severity: sev, Source: source, Message: message}
	}
	return d
}

// navWorldForAscend resolves the frame being left: the content row (through
// the cache-wide walk that finds an off-grid ephemeral visit), the doorway
// row one level out, and the framing the landing grid was left at.
func (a *App) navWorldForAscend(paneID string) *nav.LeaveWorld {
	lw := &nav.LeaveWorld{}
	p := a.tree.FindPane(paneID)
	if p == nil {
		return lw
	}
	lw.PaneScratch = a.scratchGridOf(p)
	own := p.FramingTarget()
	switch {
	case own.Content:
		if file, ok := a.descendedTile(p); ok {
			lw.DescendedTile = &file
		}
	case own.TileID != "":
		lw.DoorGridID = a.gridIDForPathFrom(own.DoorAnchor, own.DoorPath)
		if g, ok := a.c.Grid(lw.DoorGridID); ok {
			lw.DoorGridCached = true
			if t, ok := g.Tiles[own.TileID]; ok {
				lw.DoorTile = &t
			}
		}
	}
	// The viewport the ascent lands at when the frame carries none, having
	// been restored from a URL or a layout blob: the grid's persisted
	// framing, from the row that owns it.
	landing := p.Popped(1)
	if !landing.HasView() && !landing.Content {
		if cx, cy, zoom, ok := a.persistedGridView(p, landing.Anchor(), landing.Path()); ok {
			lw.LandingView = &nav.Viewport{Cx: cx, Cy: cy, Zoom: zoom}
		}
	}
	return lw
}

// urlSurfaces and shellSurfaces list the panes currently holding a live
// surface, keyed by the content they show: the input to pane.TakeOver, and
// the snapshot the close-all sweeps walk at unload. They read straight off
// the live handles, the one owner of "this pane has a surface open"; nothing
// is mirrored.
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
