package nav

import (
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/textedit"
	"github.com/josephburnett/gridwell/client/urlwalk"
	"github.com/josephburnett/gridwell/client/zoomtrans"
)

// The restore verbs: decode an address and go there. A grid the snapshot does
// not hold suspends the walk on Await{GetGrid}; no grid is asked for twice, so
// it terminates.

// restoreData travels on the continuation because the address it was decoded
// from is not re-readable: the browser may already say something else.
type restoreData struct {
	PaneID string
	State  pane.URLState
	// IDs is the URL's path, qualified with the anchor's namespace.
	IDs []string
	// Asked is every grid this restore has requested: a transport failure
	// latches nothing.
	Asked       map[string]bool
	FromHistory bool
}

// restoreFromHistory applies a browser back or forward: a reload-equivalent
// restore at the address the browser navigated to, session scaffolding reset.
func (m *Machine) restoreFromHistory(g Gesture, w World) Plan {
	var pl planner
	// Up at plan time, synchronously inside the popstate callback, before a
	// pending debounced write could clobber the browser's entry.
	m.urlRestoring = true
	pl.add(Effect{Kind: EffFlushDirtyText})
	pl.add(Effect{Kind: EffFlushFraming})
	// Navigation inside a pane tile pushes no entries, so a popstate always
	// crosses a place boundary.
	pl.add(Effect{Kind: EffLeaveLevels, Count: w.LevelDepth})
	// The focused pane can only be read after the levels swap the tree.
	pl.then(Gesture{Kind: GestureRestore, Raw: g.Raw, Reset: true})
	return pl.plan()
}

// restore decodes raw and places the pane there, the idempotent routine boot
// and popstate share. Reset asks for the popstate half: the per-pane teardown
// a reload would do, and the URL handed back at the end.
func (m *Machine) restore(g Gesture, w World) Plan {
	var pl planner
	paneID := g.PaneID
	if paneID == "" {
		paneID = w.Focus
	}
	d := &restoreData{PaneID: paneID, Asked: map[string]bool{}, FromHistory: g.Reset}
	if g.Reset {
		p, ok := w.Pane(paneID)
		if !ok {
			return m.endRestore(d, &pl)
		}
		pl.add(Effect{Kind: EffCloseMenu})
		pl.add(Effect{Kind: EffCancelTransition})
		pl.add(Effect{Kind: EffForgetPane, PaneID: paneID})
		// A restore replaces where the pane is, not how it is framed.
		pl.install(paneID, oneFrame(w.Home, p.View), nil)
		pl.add(Effect{Kind: EffRefreshOverlay})
	}
	state, err := pane.DecodeURL(g.Raw)
	if err != nil {
		state = pane.URLState{} // bad address — drop to root
	}
	d.State = state
	// The level stack stays empty above a workspace place, nesting being
	// session-only.
	if state.Workspace != "" {
		m.bootLevel(state.Workspace, &pl)
		return m.endRestore(d, &pl)
	}
	p, ok := w.Pane(paneID)
	if !ok {
		return m.endRestore(d, &pl)
	}
	// No anchor means home.
	if d.State.Anchor == "" {
		d.State.Anchor = w.Home
		if d.State.Anchor == "" {
			// Bootstrap learned no home; the error is already on the strip.
			pl.add(Effect{Kind: EffScheduleURLUpdate})
			return m.endRestore(d, &pl)
		}
	}
	pl.install(paneID, oneFrame(d.State.Anchor, p.View), nil)

	// The URL's path segments are bare well ids, qualified with the anchor's
	// namespace to match the grid's keys.
	prefix := rpc.NamespaceOf(d.State.Anchor)
	d.IDs = make([]string, len(d.State.TileIDs))
	for i, id := range d.State.TileIDs {
		d.IDs[i] = rpc.QualifyID(prefix, id)
	}
	if len(d.IDs) == 0 {
		return m.restoreRoot(d, w, &pl)
	}
	return m.restoreWalk(d, w, &pl)
}

// restoreRoot sits the pane at the anchor's root grid, at its persisted root
// view unless the address carries its own viewport.
func (m *Machine) restoreRoot(d *restoreData, w World, pl *planner) Plan {
	if m.awaitGrid(d, d.State.Anchor, stepRestoreRoot, w, pl) {
		return pl.plan()
	}
	p, ok := w.Pane(d.PaneID)
	if !ok {
		return m.endRestore(d, pl)
	}
	root := w.Restore.rootView(d.State.Anchor)
	if bv := pane.URLBootViewport(d.State, root); bv.Apply {
		zoom := liveZoom(p)
		if bv.SetZoom {
			zoom = bv.Zoom
		}
		if v, err := rpc.NewFraming(bv.Cx, bv.Cy, zoom); err == nil {
			pl.add(Effect{Kind: EffInstallPlace, PaneID: d.PaneID, Viewport: &v})
		}
	}
	pl.add(Effect{Kind: EffScheduleURLUpdate})
	return m.endRestore(d, pl)
}

func (m *Machine) restoreWalk(d *restoreData, w World, pl *planner) Plan {
	path, leaf, need := walkURL(d, w.Restore)
	if need != "" {
		if m.awaitGrid(d, need, stepRestoreWalk, w, pl) {
			return pl.plan()
		}
	}
	p, ok := w.Pane(d.PaneID)
	if !ok {
		return m.endRestore(d, pl)
	}
	// Outer frames carry no viewport, so the ascent out lands on each grid's
	// persisted framing.
	st := pane.StackAt(d.State.Anchor, path, leaf)
	v, viewed := p.View.Framing()
	if !viewed {
		v = zoomtrans.Origin
	}
	if leaf == "" {
		zoom := v.Zoom()
		if d.State.HasZoom {
			zoom = d.State.Zoom
		}
		if u, err := rpc.NewFraming(d.State.X, d.State.Y, zoom); err == nil {
			v = u
		}
		pl.install(d.PaneID, st, &v)
		return m.finishRestore(d, path, w, pl)
	}
	row, cached := leafRow(d.State.Anchor, path, leaf, w.Restore)
	in := textedit.ModeInput{TextDocument: true, CursorURL: d.State.CursorMode}
	if cached {
		in = textedit.ModeInput{TextDocument: row.TextDocument, ReadOnly: row.ReadOnly,
			Cached: true, CursorURL: d.State.CursorMode, Stored: row.TextMode}
	}
	st.TextMode = textedit.DescentMode(in)
	st.TextScrollY = float64(row.TextY)
	pl.install(d.PaneID, st, &v)
	pl.add(Effect{Kind: EffScaleContent, PaneID: d.PaneID})
	if cached {
		tok := m.mint(cont{
			Guard:   Guard{Kind: GuardAlways},
			Step:    stepRestoreCursor,
			Restore: d,
		})
		pl.add(Effect{Kind: EffAwait, Token: tok,
			Request: Request{Kind: RequestReadContent, ID: leaf}})
	}
	pl.add(Effect{Kind: EffRefreshOverlay})
	pl.add(Effect{Kind: EffReEngage, PaneID: d.PaneID, TileID: leaf})
	return m.finishRestore(d, path, w, pl)
}

// finishRestore rewrites the address in case the walk truncated it.
func (m *Machine) finishRestore(d *restoreData, path []string, w World, pl *planner) Plan {
	gid := leafGrid(d.State.Anchor, path, w.Restore)
	if _, read := w.Restore.rows(gid); !read || !d.Asked[gid] {
		pl.add(Effect{Kind: EffFetchGrid, PaneID: d.PaneID})
	}
	pl.add(Effect{Kind: EffScheduleURLUpdate})
	return m.endRestore(d, pl)
}

// endRestore closes a popstate restore. The baseline is re-seeded unseen so the
// write replaces: pushing would corrupt the history stack being traversed.
func (m *Machine) endRestore(d *restoreData, pl *planner) Plan {
	if d.FromHistory {
		m.urlRestoring = false
		m.urlPlaceSeen = false
		pl.add(Effect{Kind: EffWriteURLNow})
	}
	return pl.plan()
}

// awaitGrid suspends the restore on one grid the snapshot does not hold.
func (m *Machine) awaitGrid(d *restoreData, gridID string, s step, w World, pl *planner) bool {
	if _, ok := w.Restore.rows(gridID); ok {
		return false
	}
	if w.Restore.failed(gridID) || d.Asked[gridID] {
		return false
	}
	d.Asked[gridID] = true
	// Not pane-keyed: forgetting the pane is a step the restore performs.
	tok := m.mint(cont{Guard: Guard{Kind: GuardAlways}, Step: s, Restore: d})
	pl.add(Effect{Kind: EffAwait, Token: tok,
		Request: Request{Kind: RequestGetGrid, ID: gridID}})
	return true
}

// oneFrame is the place a jump clears a pane down to.
func oneFrame(gridID string, view rpc.View) pane.Stack {
	var s pane.Stack
	s.Reset(pane.Frame{GridID: gridID, View: view})
	return s
}

// liveZoom is the zoom p shows, zoomtrans.Origin's when it shows no view.
func liveZoom(p PaneView) float64 {
	if live, ok := p.View.Framing(); ok {
		return live.Zoom()
	}
	return zoomtrans.Origin.Zoom()
}

// walkURL runs urlwalk.Walk against the snapshot. need names the first grid it
// wanted and the snapshot does not hold.
func walkURL(d *restoreData, rw *RestoreWorld) (path []string, leaf, need string) {
	seen := map[string]map[string]urlwalk.Tile{}
	path, leaf = urlwalk.Walk(d.State.Anchor, d.IDs,
		func(gid string) (map[string]urlwalk.Tile, bool) {
			if t, ok := seen[gid]; ok {
				return t, true
			}
			rows, ok := rw.rows(gid)
			if !ok {
				// A latched or already-asked grid is a dead end, not a wait.
				if need == "" && !rw.failed(gid) && !d.Asked[gid] {
					need = gid
				}
				return nil, false
			}
			t := make(map[string]urlwalk.Tile, len(rows))
			for id, row := range rows {
				t[id] = urlwalk.Tile{ChildGridID: row.ChildGridID,
					IsWell: row.IsWell, IsContent: row.IsContent}
			}
			seen[gid] = t
			return t, true
		})
	return path, leaf, need
}

func leafRow(anchor string, path []string, leaf string, rw *RestoreWorld) (RestoreTile, bool) {
	rows, ok := rw.rows(leafGrid(anchor, path, rw))
	if !ok {
		return RestoreTile{}, false
	}
	row, ok := rows[leaf]
	return row, ok
}

// leafGrid is the grid path lands in; see pane.ResolveLeafGrid.
func leafGrid(anchor string, path []string, rw *RestoreWorld) string {
	return pane.ResolveLeafGrid(anchor, path,
		func(gid, wellID string) (string, bool, bool) {
			rows, ok := rw.rows(gid)
			if !ok {
				return "", false, false
			}
			t, ok := rows[wellID]
			if !ok {
				return "", true, false
			}
			return t.ChildGridID, true, true
		})
}
