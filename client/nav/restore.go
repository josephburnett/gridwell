package nav

import (
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/gridpath"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/textedit"
	"github.com/josephburnett/gridwell/client/urlwalk"
)

// The restore verbs: decode an address and go there.
//
// A restore is the same place model as a descent, arrived at from the other
// end — the URL (client/pane, url.go) is an ENCODING of the frame stack, and
// urlwalk.Walk is the one resolver of its ids against the user's grids. The
// walk is loose by design: a bookmarked address must degrade gracefully as
// the canvas changes underneath it.
//
// It is a token loop, not a blocking read. The walk is pure and
// deterministic, so a grid it needs and the snapshot does not hold suspends
// the whole restore on Await{GetGrid} and the walk re-runs against the warmer
// snapshot when the answer lands. Each round asks for at most one grid and
// never asks twice, so it terminates, and nothing in the machine blocks.

// restoreData is the gesture-time half of a suspended restore: what the walk
// still needs when the answer lands. It travels on the continuation because
// the address it was decoded from is not re-readable — by then the browser
// may already say something else.
type restoreData struct {
	PaneID string
	State  pane.URLState
	// IDs is the URL's path, qualified with the anchor's namespace.
	IDs []string
	// Asked is every grid this restore has already requested. A transport
	// failure latches nothing, so without it a grid that will not load would
	// be asked forever; fetchGridSync had the same one-shot rule by being one
	// call.
	Asked map[string]bool
	// FromHistory marks a popstate restore, which owns the URL until it ends.
	FromHistory bool
}

// restoreFromHistory applies a browser back or forward (popstate): a
// reload-equivalent restore of the focused pane at the address the browser
// navigated to. The session scaffolding a reload would lose — the pane's
// outer frames, live streams, selection — resets too, deliberately: back is
// navigation to a place, and the place's truth (content, framing) is
// server-owned, so what is dropped is transient scaffolding, never data.
func (m *Machine) restoreFromHistory(g Gesture, w World) Plan {
	var pl planner
	// The restore owns the URL from here until it ends. The flag goes up at
	// PLAN time, which the shim reaches synchronously inside the popstate
	// callback, before any pending debounced write can fire and clobber the
	// entry the browser just navigated to.
	m.urlRestoring = true
	// Leaving the current place: the same boundary flushes every other
	// navigation performs (pending text and framing still in their debounce
	// windows).
	pl.add(Effect{Kind: EffFlushDirtyText})
	pl.add(Effect{Kind: EffFlushFraming})
	// The popped URL names the whole place. Navigation inside a pane tile
	// never pushes entries, since the URL is constant there, so a popstate
	// always crosses a place boundary: exit any level stack through its real
	// exit path, layout flushes included, before restoring.
	pl.add(Effect{Kind: EffLeaveLevels, Count: w.LevelDepth})
	// Leaving those levels swaps the whole pane tree, so which pane is focused
	// — and everything the reset reads off it — can only be read from a world
	// gathered after it.
	pl.then(Gesture{Kind: GestureRestore, Raw: g.Raw, Reset: true})
	return pl.plan()
}

// restore decodes raw and places the pane there — the idempotent "decode an
// address and go there" routine boot and the popstate restore share. Reset
// asks for the popstate half: the per-pane teardown a reload would do, and
// the URL handed back to the browser at the end.
func (m *Machine) restore(g Gesture, w World) Plan {
	var pl planner
	// A restore is always the focused pane's; the gesture may name one anyway.
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
		// Land whatever is animating before the reset: a restore replaces the
		// place, and a transition dropped here would leave its descent half
		// done.
		pl.add(Effect{Kind: EffCancelTransition})
		pl.add(Effect{Kind: EffForgetPane, PaneID: paneID})
		// Clear the place down to one frame: the decoded place is installed
		// over it below, so a deeper frame left standing here would survive a
		// restore to a shallower place.
		st := oneFrame(w.Home, p)
		pl.add(Effect{Kind: EffInstallPlace, PaneID: paneID, Stack: &st})
		pl.add(Effect{Kind: EffRefreshOverlay})
	}
	state, err := pane.DecodeURL(g.Raw)
	if err != nil {
		state = pane.URLState{} // bad address — drop to root
	}
	d.State = state
	// A workspace place restores the innermost pane tile from its blob; the
	// level stack stays empty above it (nesting is session-only), so a bar
	// ascent falls back to the pane tile's containing grid.
	if state.Workspace != "" {
		m.bootLevel(state.Workspace, &pl)
		return m.endRestore(d, &pl)
	}
	p, ok := w.Pane(paneID)
	if !ok {
		return m.endRestore(d, &pl)
	}
	// No anchor → home, the landing page. The walk below still applies, so "/"
	// plus a viewport restores.
	if d.State.Anchor == "" {
		d.State.Anchor = w.Home
		if d.State.Anchor == "" {
			// Bootstrap could not learn any home (no plugins, no node
			// identity); the error is already on the strip, and there is
			// nothing to restore into.
			pl.add(Effect{Kind: EffScheduleURLUpdate})
			return m.endRestore(d, &pl)
		}
	}
	anchored := oneFrame(d.State.Anchor, p)
	pl.add(Effect{Kind: EffInstallPlace, PaneID: paneID, Stack: &anchored})

	// The URL's path segments are bare well ids within the anchor's grid
	// namespace; qualify them with the anchor's NAMESPACE — everything up to
	// its last segment — so they match the grid's keys. For a plain plugin
	// root ("uuid/1") that is the plugin uuid; for a remote grid reached
	// through a mount ("ssh1/rp1/1") it is the whole chain prefix.
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

// restoreRoot sits the pane at the anchor's root grid, at its PERSISTED root
// view (persistFraming's root arm writes it) unless the address carries its
// own viewport. Landing at 0,0 zoom 1 instead would be a framing the user
// never set — which is what this call site did for years, so every relaunch
// opened home at the origin no matter what the user left behind.
func (m *Machine) restoreRoot(d *restoreData, w World, pl *planner) Plan {
	if m.awaitGrid(d, d.State.Anchor, stepRestoreRoot, w, pl) {
		return pl.plan()
	}
	p, ok := w.Pane(d.PaneID)
	if !ok {
		return m.endRestore(d, pl)
	}
	root := w.Restore.rootView(d.State.Anchor)
	if bv := pane.URLBootViewport(d.State.X, d.State.Y, d.State.Zoom,
		root.Cx, root.Cy, root.Zoom); bv.Apply {
		v := Viewport{Cx: bv.Cx, Cy: bv.Cy, Zoom: p.Zoom}
		if bv.SetZoom {
			v.Zoom = bv.Zoom
		}
		pl.add(Effect{Kind: EffInstallPlace, PaneID: d.PaneID, Viewport: &v})
	}
	pl.add(Effect{Kind: EffScheduleURLUpdate})
	return m.endRestore(d, pl)
}

// restoreWalk resolves the path against the snapshot's grids and installs what
// it lands on: a grid leaf, or a content descent with its mode, scroll, body
// and go-live.
func (m *Machine) restoreWalk(d *restoreData, w World, pl *planner) Plan {
	path, leaf, need := walkURL(d, w.Restore)
	if need != "" {
		if m.awaitGrid(d, need, stepRestoreWalk, w, pl) {
			return pl.plan()
		}
	}
	p, ok := w.Pane(d.PaneID)
	if !ok {
		// The pane went away mid-walk: there is nowhere to restore into, and
		// the address is still the browser's to get back.
		return m.endRestore(d, pl)
	}
	// The decoded place, built by the one decoder: a root grid, the walked
	// doorway path, and the content leaf when there is one. The outer frames
	// carry no viewport, because nothing encodes those, so the ascent out of
	// here lands on each grid's persisted framing.
	st := pane.StackAt(d.State.Anchor, path, leaf)
	v := Viewport{Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom}
	if v.Zoom <= 0 {
		v.Zoom = 1
	}
	if leaf == "" {
		v.Cx, v.Cy = d.State.X, d.State.Y
		if d.State.Zoom > 0 {
			v.Zoom = d.State.Zoom
		}
		pl.add(Effect{Kind: EffInstallPlace, PaneID: d.PaneID, Stack: &st, Viewport: &v})
		return m.finishRestore(d, pl)
	}
	// Mode follows the one descent decision (textedit.DescentMode — a
	// read-only tile always restores RENDERED, the selectable face); an
	// address that encodes a text cursor forces text mode. Scroll restores
	// from the tile's stored text_y.
	row, cached := leafRow(d.State.Anchor, path, leaf, w.Restore)
	in := textedit.ModeInput{TextDocument: true, CursorURL: d.State.CursorMode}
	if cached {
		in = textedit.ModeInput{TextDocument: row.TextDocument, ReadOnly: row.ReadOnly,
			Cached: true, CursorURL: d.State.CursorMode, Stored: row.TextMode}
	}
	st.TextMode = textedit.DescentMode(in)
	st.TextScrollY = float64(row.TextY)
	pl.add(Effect{Kind: EffInstallPlace, PaneID: d.PaneID, Stack: &st, Viewport: &v})
	// base × the tile's content zoom (issue #82).
	pl.add(Effect{Kind: EffScaleContent, PaneID: d.PaneID})
	if cached {
		// The bytes, and the cursor the address encodes once they have seeded
		// the textarea. A row that is in no cached grid has nothing to read.
		tok := m.mint(cont{
			Guard:   Guard{Kind: GuardAlways},
			Step:    stepRestoreCursor,
			Restore: d,
		})
		pl.add(Effect{Kind: EffAwait, Token: tok,
			Request: Request{Kind: RequestReadContent, ID: leaf}})
	}
	// Refresh the overlay so the textarea (text mode) appears.
	pl.add(Effect{Kind: EffRefreshOverlay})
	// A reload lands back inside the descent, so re-engage it: the shell
	// reconnects and the url reopens, through the same one-owner decision
	// every descent applies.
	pl.add(Effect{Kind: EffReEngage, PaneID: d.PaneID, TileID: leaf})
	return m.finishRestore(d, pl)
}

// finishRestore is the tail every landed restore shares: the pane's own grid,
// and the address rewritten in case the walk truncated it.
func (m *Machine) finishRestore(d *restoreData, pl *planner) Plan {
	pl.add(Effect{Kind: EffFetchGrid, PaneID: d.PaneID})
	pl.add(Effect{Kind: EffScheduleURLUpdate})
	return m.endRestore(d, pl)
}

// endRestore closes a popstate restore: the URL is the browser's again, and
// the restored place is re-encoded onto the entry it navigated to. The
// baseline is re-seeded unseen, which makes that write a REPLACE even if the
// restore truncated the path — pushing would corrupt the stack being
// traversed. A boot restore ends with nothing to do.
func (m *Machine) endRestore(d *restoreData, pl *planner) Plan {
	if d.FromHistory {
		m.urlRestoring = false
		m.urlPlaceSeen = false
		pl.add(Effect{Kind: EffWriteURLNow})
	}
	return pl.plan()
}

// awaitGrid suspends the restore on one grid the snapshot does not hold, and
// reports whether it did. A grid already asked for in this restore is never
// asked again, so a load that keeps failing ends the walk instead of looping.
func (m *Machine) awaitGrid(d *restoreData, gridID string, s step, w World, pl *planner) bool {
	if _, ok := w.Restore.rows(gridID); ok {
		return false
	}
	if w.Restore.failed(gridID) || d.Asked[gridID] {
		return false
	}
	d.Asked[gridID] = true
	// The continuation is the RESTORE's, not the pane's: forgetting the pane
	// is a step the restore itself performs, so a pane-keyed retirement would
	// cancel the restore mid-reset and leave the URL suppressed forever. A
	// pane that really went away is handled by the steps, which end the
	// restore and hand the address back.
	tok := m.mint(cont{Guard: Guard{Kind: GuardAlways}, Step: s, Restore: d})
	pl.add(Effect{Kind: EffAwait, Token: tok,
		Request: Request{Kind: RequestGetGrid, ID: gridID}})
	return true
}

// oneFrame is the single-frame place a restore clears a pane down to: the
// grid, at the viewport the pane already has, since a restore replaces where
// the pane is and not how it is framed.
func oneFrame(gridID string, p PaneView) pane.Stack {
	var s pane.Stack
	s.Reset(pane.Frame{GridID: gridID, Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom})
	return s
}

// walkURL runs urlwalk.Walk against the snapshot. need names the first grid
// the walk wanted and the snapshot does not hold; the walk stops there, and
// the caller suspends on it.
func walkURL(d *restoreData, rw *RestoreWorld) (path []string, leaf, need string) {
	seen := map[string]map[string]urlwalk.Tile{}
	path, leaf = urlwalk.Walk(d.State.Anchor, d.IDs,
		func(gid string) (map[string]urlwalk.Tile, bool) {
			if t, ok := seen[gid]; ok {
				return t, true
			}
			rows, ok := rw.rows(gid)
			if !ok {
				// A latched or already-asked grid is a dead end, not a wait:
				// the walk stops with what it resolved, exactly as a failed
				// fetch did.
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

// leafRow is the content leaf's cached row, from the grid the walked path
// ends in. gridpath.ResolveLeafGrid owns that walk, the same one the shim
// resolves a pane's grid with, so the two cannot land in different grids.
func leafRow(anchor string, path []string, leaf string, rw *RestoreWorld) (RestoreTile, bool) {
	gid := gridpath.ResolveLeafGrid(anchor, path,
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
	rows, ok := rw.rows(gid)
	if !ok {
		return RestoreTile{}, false
	}
	row, ok := rows[leaf]
	return row, ok
}
