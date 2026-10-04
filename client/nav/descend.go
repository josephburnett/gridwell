package nav

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/anim"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/panebox"
	"github.com/josephburnett/gridwell/client/scratch"
	"github.com/josephburnett/gridwell/client/shellconn"
	"github.com/josephburnett/gridwell/client/textedit"
	"github.com/josephburnett/gridwell/client/transition"
	"github.com/josephburnett/gridwell/client/zoomtrans"
)

// descend takes the pane through the doorway tile. Which frame it pushes is
// the tile's wire declaration, never the call site's.
func (m *Machine) descend(g Gesture, w World) Plan {
	var pl planner
	p, ok := w.Pane(g.PaneID)
	if !ok || w.Door == nil {
		return pl.plan()
	}
	// A dead link is already drawn dead; a notice would be the error this
	// state replaced.
	if w.Door.DeadLink {
		return pl.plan()
	}
	// A descent arriving mid-animation lands that one first, so the segments
	// compute from its place rather than its scratch viewport.
	pl.add(Effect{Kind: EffCancelTransition, PaneID: p.ID})
	if w.Animating[p.ID] {
		pl.then(g)
		return pl.plan()
	}
	// Flush framing while the viewport still belongs to the place it
	// describes.
	pl.add(Effect{Kind: EffFlushFraming})
	switch {
	case rpc.IsWorkspaceKind(g.Door.GetKind()):
		pl.add(Effect{Kind: EffEnterLevel, PaneID: p.ID, TileID: g.Door.Id,
			Tile: g.Door})
	case rpc.IsContentDescentKind(g.Door.GetKind()):
		m.descendContent(p, g.Door, w, &pl)
	case rpc.IsWellKind(g.Door.GetKind()):
		m.descendGrid(p, g.Door, w, &pl)
	}
	return pl.plan()
}

// descendGrid plans the transition into a well's child grid: a pan and zoom to
// the well, the frame push, then an ease to the stored ViewZoom so a
// re-descent lands where the user left.
func (m *Machine) descendGrid(p PaneView, well *gridwellv1.Tile, w World, pl *planner) {
	if well.ChildGridId == "" {
		// A link whose target is not available says why; pluginhealth owns
		// the wording.
		if n := w.Door.Health; n != nil {
			pl.add(Effect{Kind: EffReport, Severity: n.Severity,
				Source: n.Source, Message: n.Message})
			return
		}
		pl.add(Effect{Kind: EffReport, Severity: errsurface.Info, Source: "descend",
			Message: "nothing to descend into: " + well.AltText})
		return
	}
	// A pane with no rect or no view has no descent to plan.
	size, ok := p.Rect.Size()
	live, viewed := p.View.Framing()
	if !ok || !viewed {
		return
	}
	from := zoomtrans.Endpoints{Path: p.Stack.Path(), Cx: live.Cx(), Cy: live.Cy(), Zoom: live.Zoom()}
	wl := zoomtrans.WellOf(well)
	next := pane.Frame{Door: well.Id}
	mid, swap, final, ok := zoomtrans.Descent(from, wl, size, w.CellPx)
	if !ok {
		return
	}
	base := p.Stack.Clone()
	if w.Door.IsLink {
		// A link crosses into another id space, so the frame carries the target
		// grid id and every path id below stays in one namespace.
		next.GridID = well.ChildGridId
		base.MenuOpen = w.MenuOpenOn == p.ID
		pl.add(Effect{Kind: EffCloseMenu})
		// The synthetic well an in-grid + menu descent goes through rounds
		// the doorway's position, so recentre on the exact footprint.
		mid.Cx = float64(well.X) + float64(well.W)/2
		mid.Cy = float64(well.Y) + float64(well.H)/2
	}
	pl.add(Effect{Kind: EffFetchGrid, GridID: well.ChildGridId})

	child := base.Clone()
	child.Push(next)

	parentDist := zoomtrans.PanDist(mid.Cx-from.Cx, mid.Cy-from.Cy, from.Zoom, w.CellPx) +
		zoomtrans.ZoomDist(from.Zoom, mid.Zoom, w.CellPx, w.ZoomDistFactor)
	childDist := zoomtrans.ZoomDist(swap.Zoom, final.Zoom, w.CellPx, w.ZoomDistFactor)
	var durations []float64
	if childDist > 0 {
		durations = anim.SplitN([]float64{parentDist, childDist}, w.TransitionMs)
	} else {
		durations = []float64{w.TransitionMs, 0}
	}

	pl.add(Effect{Kind: EffStartTransition, PaneID: p.ID, Segments: []transition.Segment{
		{
			Place:  &base,
			FromCx: from.Cx, FromCy: from.Cy, FromZoom: from.Zoom,
			ToCx: mid.Cx, ToCy: mid.Cy, ToZoom: mid.Zoom,
			DurationMs: durations[0],
		},
		{
			Place:  &child,
			FromCx: swap.Cx, FromCy: swap.Cy, FromZoom: swap.Zoom,
			ToCx: final.Cx, ToCy: final.Cy, ToZoom: final.Zoom,
			DurationMs: durations[1],
		},
	}})
}

// descendContent plans one pan-and-zoom into a content tile, pushing the frame
// at the landing.
func (m *Machine) descendContent(p PaneView, file *gridwellv1.Tile, w World, pl *planner) {
	r := p.Rect
	foot := pane.Footprint{X: file.X, Y: file.Y, W: file.W, H: file.H}
	wellCx, wellCy := foot.Center()
	live, viewed := p.View.Framing()
	if !viewed {
		return
	}
	target := panebox.FitZoom(r, file.W, file.H, w.TextSideInset, w.CellPx)
	if target < live.Zoom() {
		target = live.Zoom()
	}

	if rpc.TextDocument(file) {
		// A source-backed body's version is always 0, so a cached entry would
		// match forever: drop before fetching.
		if w.Door.ReadOnly {
			pl.add(Effect{Kind: EffDropTileContent, ContentID: rpc.ContentID(file)})
			pl.add(Effect{Kind: EffFetchGrid, PaneID: p.ID})
		}
		pl.add(Effect{Kind: EffFetchTileContent, TileID: file.Id})
	}

	base := p.Stack.Clone()
	// Stacking a visit over a live descent animates in the grid behind the
	// current content and leaves the frame on the stack.
	animBase := base.Clone()
	if animBase.Content {
		animBase.Pop()
	}
	landing := base.Clone()
	landing.Push(pane.ContentFrame(file.Id, foot, target,
		descentTextMode(file, w.Door.ReadOnly),
		float64(file.TextX), float64(file.TextY)))
	wasContent := base.Content

	// The row travels by value: an ephemeral scratch tile is in no cached grid.
	tok := m.mint(cont{
		Guard:  Guard{Kind: GuardPaneExists, PaneID: p.ID},
		Step:   stepDescendContentLand,
		PaneID: p.ID,
		TileID: file.Id,
		Tile:   file,
		Stack:  landing,
	})
	pl.add(Effect{Kind: EffStartTransition, PaneID: p.ID, Land: tok,
		Segments: []transition.Segment{
			{
				Place:  &animBase,
				FromCx: live.Cx(), FromCy: live.Cy(), FromZoom: live.Zoom(),
				ToCx: wellCx, ToCy: wellCy, ToZoom: target,
				DurationMs: w.TransitionMs,
			},
		}})
	if wasContent {
		pl.add(Effect{Kind: EffRefreshOverlay})
	}
}

func descentTextMode(file *gridwellv1.Tile, readOnly bool) string {
	return textedit.DescentMode(textedit.ModeInput{
		TextDocument: rpc.TextDocument(file), ReadOnly: readOnly,
		Cached: true, CursorURL: false, Stored: file.TextMode,
	})
}

// reEngage re-applies the auto-live verdict to a pane already in a content
// descent. The row is always refetched: that resolves a link's target and a
// tile that moved.
func (m *Machine) reEngage(g Gesture, w World) Plan {
	var pl planner
	tok := m.mint(cont{
		Guard:  Guard{Kind: GuardDescendedIn, PaneID: g.PaneID, TileID: g.TileID},
		Step:   stepReEngage,
		PaneID: g.PaneID,
		TileID: g.TileID,
	})
	pl.add(Effect{Kind: EffAwait, Token: tok,
		Request: Request{Kind: RequestGetTile, ID: g.TileID}})
	return pl.plan()
}

// followLink places the live view on a url link's target row, which owns the
// url, the history and the freeze writeback.
func (m *Machine) followLink(g Gesture, w World) Plan {
	var pl planner
	tok := m.mint(cont{
		Guard:  Guard{Kind: GuardDescendedIn, PaneID: g.PaneID, TileID: g.Door.Id},
		Step:   stepLinkTarget,
		PaneID: g.PaneID,
	})
	pl.add(Effect{Kind: EffAwait, Token: tok,
		Request: Request{Kind: RequestGetTile, ID: rpc.ContentID(g.Door)}})
	return pl.plan()
}

// healStale re-derives a restored pane's path when it no longer leads to the
// descended tile's grid. It reports whether the search started; if so the
// go-live verdict rides the answer.
func (m *Machine) healStale(paneID string, tile *gridwellv1.Tile, w World, pl *planner) bool {
	p, ok := w.Pane(paneID)
	if !ok {
		return false
	}
	// Healing an ephemeral descent would re-anchor it into the scratch grid.
	// Not known yet counts as ephemeral: the re-anchor is a durable write.
	if eph, known := scratch.Ephemeral(p.Scratch, tile.GridId); eph || !known {
		return false
	}
	if p.GridID == tile.GridId {
		return false // the path still resolves: nothing to heal
	}
	tok := m.mint(cont{
		Guard:  Guard{Kind: GuardDescendedIn, PaneID: paneID, TileID: tile.Id},
		Step:   stepHealed,
		PaneID: paneID,
		TileID: tile.Id,
		Tile:   tile,
	})
	pl.add(Effect{Kind: EffAwait, Token: tok,
		Request: Request{Kind: RequestSearch, Query: "id:" + tile.Id,
			Scope: tile.Id, Limit: 1}})
	return true
}

// landHealed re-anchors the pane at the owning root with the fresh path, at
// the zoom the pane shows; the layout persister picks it up from the live
// tree.
func landHealed(paneID string, tile *gridwellv1.Tile, wells []*gridwellv1.Tile, w World, pl *planner) {
	anchor := tile.GridId
	path := make([]string, 0, len(wells))
	if len(wells) > 0 {
		anchor = wells[0].GridId
		for _, wl := range wells {
			path = append(path, wl.Id)
		}
	}
	st := pane.StackAt(anchor, path, tile.Id)
	zoom := zoomtrans.Origin.Zoom()
	if p, ok := w.Pane(paneID); ok {
		if live, viewed := p.View.Framing(); viewed {
			zoom = live.Zoom()
		}
	}
	st.SetView(float64(tile.X)+float64(tile.W)/2, float64(tile.Y)+float64(tile.H)/2, zoom)
	pl.install(paneID, st, nil)
	pl.add(Effect{Kind: EffFetchGrid, GridID: tile.GridId})
	pl.add(Effect{Kind: EffScheduleURLUpdate})
}

// autoLiveOnDescent applies shellconn.DecideAutoLive to the just-descended
// tile; target is a link's target row once read, nil before.
func (m *Machine) autoLiveOnDescent(paneID string, tile, target *gridwellv1.Tile, w World, pl *planner) {
	// The shell facts key by session (shellconn.SessionKey).
	key, resolved := shellconn.SessionKey(tile, target)
	verdict := shellconn.DecideAutoLive(
		rpc.DescentOf(tile), w.Caps.LiveURL, w.Caps.Shells,
		tile.PreviewBlobId != 0, w.ShellAliveKnown[key], w.ShellAlive[key],
		tile.UrlFrozen)
	if !resolved && (verdict == shellconn.AutoLiveShell || verdict == shellconn.AutoLiveProbeShell) {
		// A link's session is its target's, read by the path a url link's
		// target is, and only when a shell would open.
		tok := m.mint(cont{
			Guard:  Guard{Kind: GuardDescendedIn, PaneID: paneID, TileID: tile.Id},
			Step:   stepShellLinkTarget,
			PaneID: paneID,
			TileID: tile.Id,
			Tile:   tile,
		})
		pl.add(Effect{Kind: EffAwait, Token: tok,
			Request: Request{Kind: RequestGetTile, ID: rpc.ContentID(tile)}})
		return
	}
	switch verdict {
	case shellconn.AutoLiveURL:
		pl.add(Effect{Kind: EffOpenStream, PaneID: paneID, TileID: tile.Id,
			Stream: StreamURL})
	case shellconn.AutoLiveShell:
		pl.add(Effect{Kind: EffOpenStream, PaneID: paneID, TileID: tile.Id,
			Stream: StreamShell, Key: key})
	case shellconn.AutoLiveProbeShell:
		tok := m.mint(cont{
			Guard:  Guard{Kind: GuardDescendedIn, PaneID: paneID, TileID: tile.Id},
			Step:   stepProbedShell,
			PaneID: paneID,
			TileID: tile.Id,
			Key:    key,
		})
		pl.add(Effect{Kind: EffAwait, Token: tok,
			Request: Request{Kind: RequestProbeShell, ID: rpc.ContentID(tile), Key: key}})
	}
}
