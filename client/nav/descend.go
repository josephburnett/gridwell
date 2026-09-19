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

// The descent verb: one gesture, and one switch over the doorway tile's wire
// declarations. A well, a link into another namespace and a content tile do
// not become separate descent verbs.

// descend takes the pane through the doorway tile. Which frame it pushes is
// the tile's declaration, never the call site's, and a pane tile descends the
// window a level instead, its place being a whole tree.
func (m *Machine) descend(g Gesture, w World) Plan {
	var pl planner
	p, ok := w.Pane(g.PaneID)
	if !ok || w.Door == nil {
		return pl.plan()
	}
	// A dead link has nothing on the other side and the tile is already drawn
	// dead, so a notice would be the error this state replaced. It stops
	// before the framing flush because the pane's place does not change.
	if w.Door.DeadLink {
		return pl.plan()
	}
	// A descent arriving mid-animation lands that one first, so the segments
	// below compute from the place it left rather than from the outgoing
	// animation's scratch viewport.
	pl.add(Effect{Kind: EffCancelTransition, PaneID: p.ID})
	if w.Animating[p.ID] {
		pl.then(g)
		return pl.plan()
	}
	// Flush framing while the viewport still belongs to the place it
	// describes. One place asks, so no door can forget.
	pl.add(Effect{Kind: EffFlushFraming})
	// The getter, so a gesture with no door names no kind and plans nothing.
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
// the well at Overtake, the frame push, then an ease to the stored ViewZoom so
// a re-descent lands where the user left. The time is split by motion
// distance, and the second segment is zero-length when ViewZoom is unset.
func (m *Machine) descendGrid(p PaneView, well *gridwellv1.Tile, w World, pl *planner) {
	if well.ChildGridId == "" {
		// A link whose target is not available says why rather than doing
		// nothing. pluginhealth owns the wording and the gatherer asked it.
		if n := w.Door.Health; n != nil {
			pl.add(Effect{Kind: EffReport, Severity: n.Severity,
				Source: n.Source, Message: n.Message})
			return
		}
		pl.add(Effect{Kind: EffReport, Severity: errsurface.Info, Source: "descend",
			Message: "nothing to descend into: " + well.AltText})
		return
	}
	r := p.Rect
	from := zoomtrans.Endpoints{Path: p.Stack.Path(), Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom}
	wl := zoomtrans.WellOf(well)
	next := pane.Frame{Door: well.Id}
	mid, swap, final := zoomtrans.Descent(from, wl, r.W, r.H, w.CellPx)
	base := p.Stack.Clone()
	if w.Door.IsLink {
		// A link crosses into another id space, so the frame carries the target
		// grid id: every path id below stays in one namespace, and the ascent
		// pops back onto this tile without searching the parent grid.
		next.GridID = well.ChildGridId
		// The + menu comes back with you, just as you left it.
		base.MenuOpen = w.MenuOpenOn == p.ID
		pl.add(Effect{Kind: EffCloseMenu})
		// The synthetic well an in-grid + menu descent goes through rounds
		// the doorway's position, so recentre on the exact footprint.
		mid.Cx = float64(well.X) + float64(well.W)/2
		mid.Cy = float64(well.Y) + float64(well.H)/2
	}
	pl.add(Effect{Kind: EffFetchGrid, GridID: well.ChildGridId})

	// Segments install snapshots, so the parent frame keeps the viewport the
	// user left it at while the child segment plays in the pushed frame.
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
// at the landing. Nothing is appended to the path, the tile being a leaf of
// the current grid, and the target is the fit zoom for the inner box, the
// meaningful screen area when live.
func (m *Machine) descendContent(p PaneView, file *gridwellv1.Tile, w World, pl *planner) {
	r := p.Rect
	foot := pane.Footprint{X: file.X, Y: file.Y, W: file.W, H: file.H}
	wellCx, wellCy := foot.Center()
	target := panebox.FitZoom(r, file.W, file.H, w.TextSideInset, w.CellPx)
	if target < p.Zoom {
		target = p.Zoom
	}

	// Fetch the blob eagerly so it is likely cached when the transition
	// lands. A url or serves_page tile has none: its descent is the page.
	if rpc.TextDocument(file) {
		// A source-backed body is host state, not versioned content: its
		// version is always 0, so a cache entry from the first open would
		// match forever. Drop before fetching, or the fetch does not refetch.
		if w.Door.ReadOnly {
			pl.add(Effect{Kind: EffDropTileContent, ContentID: rpc.ContentID(file)})
			pl.add(Effect{Kind: EffFetchGrid, PaneID: p.ID})
		}
		pl.add(Effect{Kind: EffFetchTileContent, TileID: file.Id})
	}

	base := p.Stack.Clone()
	// Stacking a visit over a live descent animates in the grid behind the
	// current content, whose coordinates its viewport already uses, and leaves
	// the frame on the stack so one ascent lands back on it.
	animBase := base.Clone()
	if animBase.Content {
		animBase.Pop()
	}
	landing := base.Clone()
	landing.Push(pane.ContentFrame(file.Id, foot, target,
		descentTextMode(file, w.Door.ReadOnly),
		float64(file.TextX), float64(file.TextY)))
	wasContent := base.Content

	// The descent-time row travels by value: an ephemeral scratch tile is in
	// no cached grid, so a lookup at transition end would miss it and
	// silently skip going live.
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
				FromCx: p.Cx, FromCy: p.Cy, FromZoom: p.Zoom,
				ToCx: wellCx, ToCy: wellCy, ToZoom: target,
				DurationMs: w.TransitionMs,
			},
		}})
	if wasContent {
		// The animation plays over the grid behind the outgoing content.
		pl.add(Effect{Kind: EffRefreshOverlay})
	}
}

// descentTextMode applies textedit.DescentMode, the one owner, to the
// descent-time row. A gesture descent never has the restore path's cursor URL.
func descentTextMode(file *gridwellv1.Tile, readOnly bool) string {
	return textedit.DescentMode(textedit.ModeInput{
		TextDocument: rpc.TextDocument(file), ReadOnly: readOnly,
		Cached: true, CursorURL: false, Stored: file.TextMode,
	})
}

// reEngage re-applies the auto-live verdict to a pane already in a content
// descent. The row is read first and unconditionally, since it may not be
// cached at restore time and the refetch is what resolves a link's target and
// a tile that moved. The answer runs under the moved-on rule.
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

// followLink resolves a url link's target row and places the live view on it,
// because the url, the session partition, the history and the freeze writeback
// all belong to the row that owns the content, in a grid the client has likely
// never loaded. The read runs under the moved-on rule.
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
// descended tile's grid, the tile's id being immutable and its path not. It
// reports whether the search was started, and when it was the go-live verdict
// rides the answer, so a heal precedes the re-engagement it moves.
func (m *Machine) healStale(paneID string, tile *gridwellv1.Tile, w World, pl *planner) bool {
	p, ok := w.Pane(paneID)
	if !ok {
		return false
	}
	// An ephemeral descent rides above whatever place the pane frames, so
	// healing would re-anchor it into the scratch grid. Not known yet counts
	// as ephemeral, because the re-anchor is a durable write.
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

// landHealed re-anchors the pane at the owning root with the fresh path. The
// layout persister derives the corrected layout from the live tree on its next
// tick, so the heal persists with no dedicated writer.
func landHealed(paneID string, tile *gridwellv1.Tile, wells []*gridwellv1.Tile, pl *planner) {
	anchor := tile.GridId
	path := make([]string, 0, len(wells))
	if len(wells) > 0 {
		anchor = wells[0].GridId
		for _, wl := range wells {
			path = append(path, wl.Id)
		}
	}
	st := pane.StackAt(anchor, path, tile.Id)
	// Centre on the tile in its new grid, so ascending lands looking at it
	// rather than at a stale offset.
	st.Cx = float64(tile.X) + float64(tile.W)/2
	st.Cy = float64(tile.Y) + float64(tile.H)/2
	pl.add(Effect{Kind: EffInstallPlace, PaneID: paneID, Stack: &st})
	pl.add(Effect{Kind: EffFetchGrid, GridID: tile.GridId})
	pl.add(Effect{Kind: EffScheduleURLUpdate})
}

// autoLiveOnDescent applies shellconn.DecideAutoLive to the just-descended
// tile. It is the one auto-live owner, and the refresh affordance is the retry
// wherever it stays frozen.
func (m *Machine) autoLiveOnDescent(paneID string, tile *gridwellv1.Tile, w World, pl *planner) {
	// The shell facts key by content id, so a link attaches its target's
	// session. The refresh button's visibility reads the same, so the two
	// cannot disagree about a dead session.
	cid := rpc.ContentID(tile)
	switch shellconn.DecideAutoLive(
		rpc.WebContent(tile), tile.Kind == rpc.KindShell,
		w.Caps.LiveURL, w.Caps.Shells,
		tile.PreviewBlobId != 0, w.ShellAliveKnown[cid], w.ShellAlive[cid],
		tile.UrlFrozen) {
	case shellconn.AutoLiveURL:
		pl.add(Effect{Kind: EffOpenStream, PaneID: paneID, TileID: tile.Id,
			Stream: StreamURL})
	case shellconn.AutoLiveShell:
		pl.add(Effect{Kind: EffOpenStream, PaneID: paneID, TileID: tile.Id,
			Stream: StreamShell})
	case shellconn.AutoLiveProbeShell:
		// The probe is async and the user may move on.
		tok := m.mint(cont{
			Guard:  Guard{Kind: GuardDescendedIn, PaneID: paneID, TileID: tile.Id},
			Step:   stepProbedShell,
			PaneID: paneID,
			TileID: tile.Id,
		})
		pl.add(Effect{Kind: EffAwait, Token: tok,
			Request: Request{Kind: RequestProbeShell, ID: cid}})
	}
}
