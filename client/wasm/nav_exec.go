//go:build js && wasm

package main

// The navigation executor: one switch, one func per effect, each a direct
// move of the body it replaces. Everything impure a descent or an ascent does
// — every RPC, every DOM write, every js.Value — is on this side of the seam.
// Nothing here decides anything; client/nav does.

import (
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/nav"
	"github.com/josephburnett/gridwell/client/transition"
)

// runNav executes a plan in order and draws. There is no redraw effect: the
// executor draws once after a plan, always.
func (a *App) runNav(plan nav.Plan) {
	for _, e := range plan.Effects {
		a.runNavEffect(e)
	}
	a.draw()
}

func (a *App) runNavEffect(e nav.Effect) {
	switch e.Kind {
	case nav.EffInstallPlace:
		a.navInstallPlace(e)
	case nav.EffClearSelection:
		a.clearSelected(e.PaneID)
	case nav.EffFlushFraming:
		a.flushFramingSave()
	case nav.EffPersistFraming:
		a.navPersistFraming(e)
	case nav.EffSaveText:
		a.navSaveText(e)
	case nav.EffCancelTransition:
		if e.PaneID == "" {
			a.trans.CancelAll()
			return
		}
		a.trans.Cancel(e.PaneID)
	case nav.EffStartTransition:
		a.navStartTransition(e)
	case nav.EffCloseStream:
		a.navCloseStream(e)
	case nav.EffOpenStream:
		a.navOpenStream(e)
	case nav.EffRefreshOverlay:
		a.refreshFileOverlay()
	case nav.EffScaleContent:
		if p := a.tree.FindPane(e.PaneID); p != nil {
			p.TextZoom = a.textScaleFor(p) // base × content zoom (issue #82)
		}
	case nav.EffFetchGrid:
		a.fetchGrid(a.navGridID(e))
	case nav.EffFetchTileContent:
		a.fetchTileContent(e.TileID)
	case nav.EffDropTileContent:
		a.c.DropTileContent(e.ContentID)
	case nav.EffAwait:
		a.navAwait(e)
	case nav.EffOpenMenu:
		a.menu.Open(e.PaneID)
		if p := a.tree.FindPane(e.PaneID); p != nil {
			p.MenuOpen = false
		}
	case nav.EffCloseMenu:
		a.menu.Close()
	case nav.EffScheduleURLUpdate:
		a.scheduleURLUpdate()
	case nav.EffDeleteEphemeral:
		a.deleteEphemeralTile(e.GridID, e.TileID)
	case nav.EffReport:
		a.reportErr(e.Severity, e.Source, e.Message)
	case nav.EffEnterLevel:
		if p := a.tree.FindPane(e.PaneID); p != nil {
			tile := e.Tile
			a.descendLevel(p, &tile)
		}
	case nav.EffLeaveLevels:
		a.ascendLevels(e.Count)
	case nav.EffReEngage:
		a.autoLiveOnRestore(e.PaneID, e.TileID)
	default:
		// The vocabulary is frozen ahead of the phases that emit the rest of
		// it. An effect with no executor is a bug in the machine, not a
		// silent no-op: it surfaces like every other failure.
		a.reportErr(errsurface.Error, "nav", "no executor for this navigation effect")
	}
}

// navInstallPlace installs a pane's place, and the viewport it lands at when
// the plan named one. A nil viewport keeps the pane's own — out of a content
// descent with nothing to restore, where the viewport is already in the
// landing grid's coordinates.
func (a *App) navInstallPlace(e nav.Effect) {
	p := a.tree.FindPane(e.PaneID)
	if p == nil {
		return
	}
	if e.Stack != nil {
		p.Stack = e.Stack.Clone()
	}
	if e.Viewport != nil {
		p.Cx, p.Cy, p.Zoom = e.Viewport.Cx, e.Viewport.Cy, e.Viewport.Zoom
	}
}

// navPersistFraming resolves the row the framing owner names and writes
// through the one framing writeback. The doorway arm mutates the row in place
// and patches the cache; the machine projects the same write onto its own
// copy so the ascent it calibrates matches.
func (a *App) navPersistFraming(e nav.Effect) {
	p := a.tree.FindPane(e.PaneID)
	if p == nil {
		return
	}
	if !e.Door {
		a.persistFraming(p, nil, "", nil)
		return
	}
	g, ok := a.c.Grid(a.gridIDForPathFrom(e.Owner.DoorAnchor, e.Owner.DoorPath))
	if !ok {
		return
	}
	t, ok := g.Tiles[e.Owner.TileID]
	if !ok {
		return
	}
	a.persistFraming(p, &t, e.Owner.DoorAnchor, e.Owner.DoorPath)
}

// navSaveText posts the editor buffer and the framed window for the content
// tile the pane is leaving. The row is re-resolved through the same cache-wide
// walk the gatherer used, so an off-grid ephemeral visit still saves.
func (a *App) navSaveText(e nav.Effect) {
	p := a.tree.FindPane(e.PaneID)
	if p == nil {
		return
	}
	file, ok := a.descendedTile(p)
	if !ok || file.ID != e.TileID {
		return
	}
	a.saveTextBeforeAscent(p, file)
}

// navStartTransition hands the segments to the per-pane set. A landing
// continuation is resumed from OnComplete, which runs whether the animation
// finished or was cut short — a cancelled transition still lands.
func (a *App) navStartTransition(e nav.Effect) {
	tr := &transition.Transition{
		PaneID:      e.PaneID,
		Segments:    e.Segments,
		TraceTileID: e.TraceTileID,
	}
	if tok := e.Land; tok != 0 {
		tr.OnComplete = func() { a.runNav(a.nav.Land(tok, a.navWorldCommon())) }
	}
	a.startTransition(tr)
}

func (a *App) navCloseStream(e nav.Effect) {
	if e.Streams == nav.StreamURL || e.Streams == nav.StreamBoth {
		if t := e.FreezeOnto; t != nil {
			a.closeURLStreamTo(e.PaneID, &freezeTarget{tileID: t.TileID, gridID: t.GridID}, e.Freeze)
		} else {
			a.closeURLStream(e.PaneID, e.Freeze)
		}
	}
	if e.Streams == nav.StreamShell || e.Streams == nav.StreamBoth {
		// Capture the JPEG, persist it as the frozen preview, close the
		// socket: closeShellStream handles all three.
		a.closeShellStream(e.PaneID, e.Freeze)
	}
}

func (a *App) navOpenStream(e nav.Effect) {
	p := a.tree.FindPane(e.PaneID)
	if p == nil {
		return
	}
	switch e.Stream {
	case nav.StreamURL:
		a.openURLStream(p, e.TileID)
	case nav.StreamShell:
		a.openShellStream(p, e.TileID)
	}
}

// navGridID resolves a grid fetch's target: the id the plan named, or — for
// "the grid this pane's place names" — the walk only this side can do, since
// it reads the cache and kicks its own fetches.
func (a *App) navGridID(e nav.Effect) string {
	if e.GridID != "" {
		return e.GridID
	}
	if p := a.tree.FindPane(e.PaneID); p != nil {
		return a.gridIDForPane(p)
	}
	return ""
}

// navAwait starts the async read a continuation is waiting on and feeds the
// answer back with its token. The machine re-evaluates the guard then, so
// nothing here re-checks whether the user moved on.
func (a *App) navAwait(e nav.Effect) {
	tok := e.Token
	switch e.Request.Kind {
	case nav.RequestProbeShell:
		a.probeShellSessionAlive(e.Request.ID, func(alive bool) {
			a.runNav(a.nav.Resume(tok, nav.Result{OK: true, Alive: alive}, a.navWorldCommon()))
		})
	default:
		a.reportErr(errsurface.Error, "nav", "no executor for this navigation request")
	}
}
