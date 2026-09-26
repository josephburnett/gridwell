//go:build js && wasm

package main

// The navigation executor: one switch, one func per effect. Everything impure
// a descent or an ascent does is on this side of the seam, and nothing here
// decides anything.

import (
	"context"

	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/inflight"
	"github.com/josephburnett/gridwell/client/nav"
	"github.com/josephburnett/gridwell/client/transition"
)

// runNav executes a plan in order and draws. There is no redraw effect: the
// executor always draws once after a plan.
func (a *App) runNav(plan nav.Plan) {
	for _, e := range plan.Effects {
		a.runNavEffect(e)
	}
	// The machine owns "a level descent is pending", so the install and a
	// failed descent both end the capture animation without remembering to.
	if !a.nav.LevelPending() {
		a.overlays.wsExpand = nil
	}
	a.draw()
}

func (a *App) runNavEffect(e nav.Effect) {
	switch e.Kind {
	case nav.EffInstallPlace:
		a.navInstallPlace(e)
	case nav.EffClearSelection:
		a.clearSelected(e.PaneID)
	case nav.EffForgetPane:
		a.forgetPane(e.PaneID)
	case nav.EffRelocatePane:
		a.navRelocatePane(e)
	case nav.EffInstallLevel:
		a.navInstallLevel(e)
	case nav.EffPopLevel:
		a.navPopLevel(e)
	case nav.EffFlushLayout:
		a.flushWorkspaceSave()
	case nav.EffFlushDroppedSubtree:
		a.flushDroppedSubtree(a.tree.Root)
	case nav.EffFlushFraming:
		a.flushFramingSave()
	case nav.EffFlushDirtyText:
		a.flushDirtyText()
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
	case nav.EffPlaceURLView:
		a.placeURLView(e.PaneID, e.Tile)
	case nav.EffRefreshOverlay:
		a.refreshFileOverlay()
	case nav.EffScaleContent:
		if p := a.tree.FindPane(e.PaneID); p != nil {
			p.TextZoom = a.textScaleFor(p) // base times content zoom
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
	case nav.EffWriteURLNow:
		a.writeURLNow()
	case nav.EffPlaceCursor:
		a.placeCursorAt(e.Col, e.Row)
	case nav.EffDeleteEphemeral:
		a.deleteEphemeralTile(e.GridID, e.TileID)
	case nav.EffReport:
		a.reportErr(e.Severity, e.Source, e.Message)
	case nav.EffEnterLevel:
		// Its own gesture, so it is planned against a world gathered after
		// the effects above it, the framing flush among them.
		a.runGesture(nav.Gesture{Kind: nav.GestureEnterLevel, PaneID: e.PaneID, Door: e.Tile})
	case nav.EffLeaveLevels:
		a.runGesture(nav.Gesture{Kind: nav.GestureLeaveLevels, Count: e.Count})
	case nav.EffReEngage:
		a.navReEngage(e.PaneID, e.TileID)
	default:
		// An effect with no executor is a bug in the machine, not a silent
		// no-op.
		a.reportErr(errsurface.Error, "nav", "no executor for this navigation effect")
	}
}

// navInstallPlace installs a pane's place and the viewport the plan named. A
// nil viewport keeps the pane's own, which is already in the landing grid's
// coordinates.
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
// through the one framing writeback. The machine projects the same write onto
// its own copy, so the ascent it calibrates matches.
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
	a.persistFraming(p, t, e.Owner.DoorAnchor, e.Owner.DoorPath)
}

// navSaveText posts the editor buffer and framed window for the tile the pane
// is leaving. The row is re-resolved through the same cache-wide walk the
// gatherer used, so an off-grid ephemeral visit still saves.
func (a *App) navSaveText(e nav.Effect) {
	p := a.tree.FindPane(e.PaneID)
	if p == nil {
		return
	}
	file, ok := a.descendedTile(p)
	if !ok || file.Id != e.TileID {
		return
	}
	a.saveTextBeforeAscent(p, file)
}

// navStartTransition hands the segments to the per-pane set. A landing
// continuation resumes from OnComplete, which runs whether the animation
// finished or was cut short, so a cancelled transition still lands.
func (a *App) navStartTransition(e nav.Effect) {
	if e.Expand {
		// The capture animation rides the same clock as the transition
		// beside it. render.go draws it; the machine's pending level decides
		// how long.
		if p := a.tree.FindPane(e.PaneID); p != nil {
			dd := paneToDragdrop(p, paneRectFor(a, p))
			x0, y0 := dd.CellToScreen(float64(e.Tile.X), float64(e.Tile.Y))
			x1, y1 := dd.CellToScreen(float64(e.Tile.X+e.Tile.W), float64(e.Tile.Y+e.Tile.H))
			a.overlays.wsExpand = &wsExpandState{x: x0, y: y0, w: x1 - x0, h: y1 - y0, startMs: nowMs()}
		}
	}
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
		// closeShellStream captures the JPEG, persists it and closes the
		// socket.
		a.closeShellStream(e.PaneID, e.Freeze)
	}
}

// navRelocatePane is the promote gesture's landing: pane.RelocateTo is the
// one mover.
func (a *App) navRelocatePane(e nav.Effect) {
	p := a.tree.FindPane(e.PaneID)
	dest := a.tree.FindPane(e.DestPaneID)
	if p == nil || dest == nil {
		return
	}
	p.RelocateTo(dest, e.TileID, e.Foot, e.Zoom)
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

// navGridID resolves a grid fetch's target: the id the plan named, or the
// place walk only this side can do, since it reads the cache and kicks its
// own fetches.
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
// answer back with its token. The machine re-evaluates the guard, so nothing
// here re-checks whether the user moved on.
func (a *App) navAwait(e nav.Effect) {
	tok := e.Token
	switch e.Request.Kind {
	case nav.RequestProbeShell:
		a.probeShellSessionAlive(e.Request.ID, func(alive bool) {
			a.runNav(a.nav.Resume(tok, nav.Result{OK: true, Alive: alive}, a.navWorldCommon()))
		})
	case nav.RequestGetTile:
		id := e.Request.ID
		// Claim-free, since the machine waits on its own answer, but
		// bounded: a read the network swallows would leave the continuation
		// owed forever.
		a.await(tok, inflight.Bounded, a.navWorldCommon, func(ctx context.Context) nav.Result {
			tile, err := a.cl.GetTile(ctx, id)
			if err != nil {
				// Whether the failure is worth a notice is the step's call,
				// so the text rides the answer rather than surfacing here.
				return nav.Result{Err: rpcErrText(err)}
			}
			// The row lands in the cache first, so the place it heals to and
			// the row the renderer draws are the same answer.
			a.c.UpdateTile(tile.GridId, tile)
			return nav.Result{OK: true, Tile: tile}
		})
	case nav.RequestGetGrid:
		id := e.Request.ID
		// Claim-free, because a background fetch for the same grid must not
		// turn the walk into a no-op, but bounded: a boot that waits forever
		// on a dead socket is a blank screen.
		a.await(tok, a.fetch.grids.Context, a.navWorldForRestore,
			func(ctx context.Context) nav.Result {
				return nav.Result{OK: a.loadGrid(ctx, id) == nil}
			})
	case nav.RequestReadContent:
		id := e.Request.ID
		// Claim-free, like the walk above, and bounded the same way.
		a.await(tok, a.fetch.contents.Context, a.navWorldCommon,
			func(ctx context.Context) nav.Result {
				// loadTileContent seeds the textarea from the body, and the
				// cursor this path adds goes after that.
				return nav.Result{OK: a.loadTileContent(ctx, id, func() {}) == nil}
			})
	case nav.RequestReadLayout:
		id := e.Request.ID
		// The bytes go straight back to the machine, which owns the codec
		// call: a layout is not a document, so it never seeds the text
		// overlay and is not cached as a body.
		a.await(tok, inflight.Bounded, a.navWorldCommon, func(ctx context.Context) nav.Result {
			data, _, _, err := a.cl.ReadContent(ctx, id)
			if err != nil {
				return nav.Result{Err: rpcErrText(err)}
			}
			return nav.Result{OK: true, Data: data}
		})
	case nav.RequestSearch:
		req := e.Request
		// A search the network swallows resolves as no result on the
		// deadline, which the machine already handles, rather than a walk
		// that never resumes.
		a.await(tok, inflight.Bounded, a.navWorldCommon, func(ctx context.Context) nav.Result {
			res, err := a.cl.Search(ctx, req.Query, req.Scope, int32(req.Limit))
			if err != nil || len(res) == 0 {
				return nav.Result{}
			}
			return nav.Result{OK: true, Wells: res[0].Path}
		})
	default:
		a.reportErr(errsurface.Error, "nav", "no executor for this navigation request")
	}
}

// await runs one navigation read off the main goroutine and resumes the
// continuation with what it answered. bound is the read's deadline and world
// the snapshot the machine re-plans against, because the restore walk plans
// against a different one from every other read.
func (a *App) await(tok nav.Token, bound func() (context.Context, context.CancelFunc),
	world func() nav.World, call func(context.Context) nav.Result) {
	go func() {
		ctx, cancel := bound()
		defer cancel()
		res := call(ctx)
		a.runNav(a.nav.Resume(tok, res, world()))
	}()
}
