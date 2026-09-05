package nav

import (
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/anim"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/panebox"
	"github.com/josephburnett/gridwell/client/shellconn"
	"github.com/josephburnett/gridwell/client/textedit"
	"github.com/josephburnett/gridwell/client/transition"
	"github.com/josephburnett/gridwell/client/zoomtrans"
)

// The descent verb.
//
// Physically there is one gesture — go through a doorway. The place model
// says the same thing (client/pane, place.go): a descent pushes a frame. The
// data model's ownership boundaries — a well, a link into another namespace,
// a content tile — are wire declarations on the doorway tile, read in one
// switch. They do not become separate descent verbs.

// descend takes the pane through the doorway tile. Which kind of frame it
// pushes is the tile's declaration, never the call site's. A well or a link
// pushes a grid frame; a text, url, shell or page tile pushes a content
// frame, because the tile is the place; and a pane tile descends the window a
// level instead, its place being a whole tree.
func (m *Machine) descend(g Gesture, w World) Plan {
	var pl planner
	p, ok := w.Pane(g.PaneID)
	if !ok || w.Door == nil {
		return pl.plan()
	}
	// A dead link is not a doorway: it points into a namespace this node does
	// not declare, so there is nothing on the other side to descend into. It
	// does nothing, quietly — the tile is already drawn dead, and that is the
	// answer. A notice here would be the error this state replaced, repeated
	// on every click. It stops in front of the framing flush because nothing
	// about the pane's place changes.
	if w.Door.DeadLink {
		return pl.plan()
	}
	// A descent that arrives while this pane is still animating a previous
	// one lands that one first: its frame push happened, and the segments
	// below are computed from the place it left, not from the outgoing
	// animation's scratch viewport. Stacking a visit over a live descent is
	// exactly this case — which is why the rest of the plan waits for a fresh
	// world.
	pl.add(Effect{Kind: EffCancelTransition, PaneID: p.ID})
	if w.Animating[p.ID] {
		pl.then(g)
		return pl.plan()
	}
	// The pane is about to change place: flush framing still inside the
	// settle window, while the viewport still belongs to the place it
	// describes. One place asks, so no door can forget.
	pl.add(Effect{Kind: EffFlushFraming})
	switch {
	case rpc.IsWorkspaceKind(g.Door.Kind):
		pl.add(Effect{Kind: EffEnterLevel, PaneID: p.ID, TileID: g.Door.ID,
			Tile: g.Door})
	case rpc.IsContentDescentKind(g.Door.Kind):
		m.descendContent(p, g.Door, w, &pl)
	case rpc.IsWellKind(g.Door.Kind):
		m.descendGrid(p, g.Door, w, &pl)
	}
	return pl.plan()
}

// descendGrid plans the two-segment transition into a well's child grid, with
// the grid frame pushed at the swap.
//
// Phases:
//
//	A. Combined pan+zoom in parent to (wellCenter, OvertakeZoom).
//	B. Atomic install of the calibrated child state at the frame push.
//	C. (Optional) animate the child to the well's stored ViewZoom so
//	   re-descent lands at the same zoom the user left at. Only fires when
//	   well.ViewZoom > 0; the default for never-entered wells is 0
//	   (calibrated zoom).
//
// Total time is split between A and C proportional to motion distance so
// neither feels rushed. C is zero-length when ViewZoom is unset.
func (m *Machine) descendGrid(p PaneView, well rpc.Tile, w World, pl *planner) {
	if well.ChildGridID == "" {
		// A link tile whose target is not available: a broken or rootless
		// plugin, or a connection whose remote has not answered. Say why
		// instead of silently doing nothing; pluginhealth owns the wording,
		// and the gatherer has already asked it.
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
	wl := zoomtrans.WellOf(&well)
	next := pane.Frame{Door: well.ID}
	mid, swap, final := zoomtrans.Descent(from, wl, r.W, r.H, w.CellPx)
	base := p.Stack.Clone()
	if w.Door.IsLink {
		// A link crosses into another id space: a plugin link tile, a mounted
		// well, a cross-plugin clone. The frame carries the target grid id —
		// the one place that fact is authoritative — so every path id below
		// it stays within one namespace, and the ascent pops back onto this
		// very tile without searching the parent grid for a well whose child
		// matches the anchor.
		next.GridID = well.ChildGridID
		// The + menu comes back with you, just as you left it.
		base.MenuOpen = w.MenuOpenOn == p.ID
		pl.add(Effect{Kind: EffCloseMenu})
		// The synthetic well an in-grid + menu descent goes through rounds
		// the launcher tile's position; recentre the parent zoom on the exact
		// footprint center so the descent lands square on it.
		mid.Cx = float64(well.X) + float64(well.W)/2
		mid.Cy = float64(well.Y) + float64(well.H)/2
	}
	pl.add(Effect{Kind: EffFetchGrid, GridID: well.ChildGridID})

	// The place each segment plays in: the parent zoom happens where the pane
	// already is, and the child segment plays in the pushed frame. Because
	// segments install snapshots, the parent frame keeps the viewport the
	// user actually left it at.
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
		// A: parent pan+zoom toward the well/footprint center at Overtake.
		{
			Place:  &base,
			FromCx: from.Cx, FromCy: from.Cy, FromZoom: from.Zoom,
			ToCx: mid.Cx, ToCy: mid.Cy, ToZoom: mid.Zoom,
			DurationMs: durations[0],
		},
		// C: after the frame push, ease the child zoom out to the stored
		// ratio (zero-length when swap == final).
		{
			Place:  &child,
			FromCx: swap.Cx, FromCy: swap.Cy, FromZoom: swap.Zoom,
			ToCx: final.Cx, ToCy: final.Cy, ToZoom: final.Zoom,
			DurationMs: durations[1],
		},
	}})
}

// descendContent plans a pane's zoom into a content tile (text, url, shell,
// page) as a single concurrent pan+zoom motion, with the content frame pushed
// at the landing. Unlike a grid descent nothing is appended to the path — the
// tile lives in the current grid as a leaf — and the meaningful screen area in
// live mode is the inner box (textarea region), not the full pane, so the
// descent targets the fit zoom that makes the footprint fill it. At the frame
// push the footprint screen size is the inner box, and the live TextZoom is
// reconstructed from the tile's intrinsic ViewZoom ratio for visual
// continuity.
func (m *Machine) descendContent(p PaneView, file rpc.Tile, w World, pl *planner) {
	r := p.Rect
	foot := pane.Footprint{X: file.X, Y: file.Y, W: file.W, H: file.H}
	wellCx, wellCy := foot.Center()
	target := panebox.FitZoom(r, file.W, file.H, w.TextSideInset, w.CellPx)
	if target < p.Zoom {
		target = p.Zoom
	}

	// Eagerly fetch the blob so it's likely cached by the time the transition
	// lands. URL tiles don't have a blob; their preview path goes through the
	// url preview instead — and so does a serves_page tile's (its descent is
	// the page, not the document body).
	if file.TextDocument() {
		// Source-backed bodies (fs files, the proc @info tile) are host
		// state, not versioned content: their version is always 0, so a cache
		// entry from the first open would match forever and the descent would
		// show stale bytes however the file changed on disk. Every open
		// re-reads — it is all read-only — so drop before fetching, or the
		// fetch does not refetch.
		if w.Door.ReadOnly {
			pl.add(Effect{Kind: EffDropTileContent, ContentID: file.ContentID()})
			pl.add(Effect{Kind: EffFetchGrid, PaneID: p.ID})
		}
		pl.add(Effect{Kind: EffFetchTileContent, TileID: file.ID})
	}

	base := p.Stack.Clone()
	// Stacking a visit over a live descent (a url opened from a shell): the
	// animation plays in the grid behind the current content, since a content
	// frame's viewport is already in that grid's coordinates, while the frame
	// itself stays on the stack, so one ascent lands right back on it. No
	// stash, no second stack.
	animBase := base.Clone()
	if animBase.Content {
		animBase.Pop()
	}
	landing := base.Clone()
	landing.Push(pane.ContentFrame(file.ID, foot, target,
		descentTextMode(file, w.Door.ReadOnly),
		float64(file.TextX), float64(file.TextY)))
	wasContent := base.Content

	// The descent-time row travels on the continuation BY VALUE: an ephemeral
	// scratch-grid tile is in no cached grid, so a cache lookup at transition
	// end would miss it and silently skip going live.
	tok := m.mint(cont{
		Guard:  Guard{Kind: GuardPaneExists, PaneID: p.ID},
		Step:   stepDescendContentLand,
		PaneID: p.ID,
		TileID: file.ID,
		Tile:   file,
		Stack:  landing,
	})
	pl.add(Effect{Kind: EffStartTransition, PaneID: p.ID, Land: tok,
		Segments: []transition.Segment{
			// Single combined pan+zoom segment: pan to the file center while
			// simultaneously zooming to the overtake target.
			{
				Place:  &animBase,
				FromCx: p.Cx, FromCy: p.Cy, FromZoom: p.Zoom,
				ToCx: wellCx, ToCy: wellCy, ToZoom: target,
				DurationMs: w.TransitionMs,
			},
		}})
	if wasContent {
		// The outgoing content's overlay goes now: the animation plays over
		// the grid behind it.
		pl.add(Effect{Kind: EffRefreshOverlay})
	}
}

// descentTextMode applies textedit.DescentMode, the one owner, to the
// descent-time row. cursorURL is the restore path's extra input (an address
// that encodes a text cursor), which a gesture descent never has.
func descentTextMode(file rpc.Tile, readOnly bool) string {
	return textedit.DescentMode(textedit.ModeInput{
		TextDocument: file.TextDocument(), ReadOnly: readOnly,
		Cached: true, CursorURL: false, Stored: file.TextMode,
	})
}

// reEngage re-applies the auto-live verdict to a pane that is already sitting
// in a content descent: the restore paths' arm of the one go-live owner. The
// row was read asynchronously, so the pane may have moved on, and where the
// user went is never overridden.
func (m *Machine) reEngage(g Gesture, w World) Plan {
	var pl planner
	guard := Guard{Kind: GuardDescendedIn, PaneID: g.PaneID, TileID: g.Door.ID}
	if !guard.holds(w) {
		return pl.plan()
	}
	m.autoLiveOnDescent(g.PaneID, g.Door, w, &pl)
	return pl.plan()
}

// autoLiveOnDescent applies the shellconn.DecideAutoLive verdict for the
// just-descended tile: open the url view, attach or create the shell PTY,
// probe an unknown shell session first, or stay frozen — text, browser hosts,
// dead sessions. It is the one auto-live owner, and the refresh affordances
// are the retry for the cases it stays frozen on.
//
// The caller has already established that the pane is descended in tile: the
// descent's landing installs that very place one effect earlier, and the
// restore path re-checks with a DescendedIn guard before it resumes.
func (m *Machine) autoLiveOnDescent(paneID string, tile rpc.Tile, w World, pl *planner) {
	// The shell facts key by the content id, so a link attaches its target's
	// session: the same reads the refresh button's visibility does, so the
	// two decisions cannot disagree about a dead session.
	cid := tile.ContentID()
	switch shellconn.DecideAutoLive(
		tile.WebContent(), tile.Kind == rpc.KindShell,
		w.Caps.LiveURL, w.Caps.LiveShell,
		tile.PreviewBlobID != 0, w.ShellAliveKnown[cid], w.ShellAlive[cid],
		tile.URLFrozen) {
	case shellconn.AutoLiveURL:
		pl.add(Effect{Kind: EffOpenStream, PaneID: paneID, TileID: tile.ID,
			Stream: StreamURL})
	case shellconn.AutoLiveShell:
		pl.add(Effect{Kind: EffOpenStream, PaneID: paneID, TileID: tile.ID,
			Stream: StreamShell})
	case shellconn.AutoLiveProbeShell:
		// The probe is async and the user may move on: the continuation
		// carries the one guard that says so.
		tok := m.mint(cont{
			Guard:  Guard{Kind: GuardDescendedIn, PaneID: paneID, TileID: tile.ID},
			Step:   stepProbedShell,
			PaneID: paneID,
			TileID: tile.ID,
		})
		pl.add(Effect{Kind: EffAwait, Token: tok,
			Request: Request{Kind: RequestProbeShell, ID: cid}})
	}
}
