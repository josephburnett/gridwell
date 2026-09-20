//go:build js && wasm

package main

import (
	"google.golang.org/protobuf/proto"

	"context"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"strings"
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/cadence"
	"github.com/josephburnett/gridwell/client/nav"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/textcursor"
	"github.com/josephburnett/gridwell/client/textedit"
	"github.com/josephburnett/gridwell/client/zoomtrans"
)

// scheduleFramingSave arms the debounced framing persister from draw(). Every
// state change redraws, so there is no per-gesture hook to forget. Writing
// only at ascent would lose the viewport whenever a grid is left another
// way.
func (a *App) scheduleFramingSave() {
	a.persist.sched.framingSave.Arm(cadence.FramingSaveMs)
}

// flushFramingSave persists every pane's settled grid framing. persistFraming
// owns the mid-transition refusal, and draw() re-arms the debounce, so an
// animating pane's flush lands after its animation while quiet siblings
// persist on time.
func (a *App) flushFramingSave() {
	a.persist.framingFlushes++
	// One active surface per grid: among panes showing the same grid only
	// the focused one writes. pane.FramingWriters is the rule.
	var pgs []pane.PaneGrid
	a.tree.Walk(func(p *pane.Pane) {
		pgs = append(pgs, pane.PaneGrid{PaneID: p.ID, GridID: a.gridIDForPane(p)})
	})
	writers := pane.FramingWriters(pgs, a.tree.Focus)
	a.tree.Walk(func(p *pane.Pane) {
		if writers[p.ID] {
			a.persistPaneFraming(p)
		}
	})
	a.flushWellWheelSaves()
}

// flushWellWheelSaves posts the settled hover-wheel well zooms from the
// pending drift state, the one owner of the not-yet-persisted view. Reading
// the cache row instead would let a refetch inside the settle window revert
// the wheel.
func (a *App) flushWellWheelSaves() {
	for id, st := range a.persist.wellWheelPending {
		gid := st.gridID
		delete(a.persist.wellWheelPending, id)
		tileID := id
		req := &gridwellv1.SetFramingRequest{
			TileId: tileID, Cx: st.cx, Cy: st.cy, Zoom: st.ratio,
		}
		// The unload transport is the dispatcher's business, so a parked
		// framing write reaches the beacon path too.
		a.postFramingPersist("SetFraming", gid, tileID,
			func(ctx context.Context) error {
				_, err := a.cl.SetFraming(ctx, req)
				return err
			},
			func() (string, []byte, string) {
				path, body := rpc.SetFramingBeacon(req)
				return path, body, rpc.BeaconJSONType
			})
	}
}

// persistPaneFraming writes pane p's current place framing, the same write an
// ascent flushes. Which row owns it is pane.FramingTarget's projection. A
// no-op when the place is unresolvable; the next settle retries.
func (a *App) persistPaneFraming(p *pane.Pane) {
	own := p.FramingTarget()
	switch {
	case own.Content:
		a.persistTextScroll(p)
	case own.TileID == "":
		a.persistFraming(p, nil, "", nil)
	default:
		gid := a.gridIDForPathFrom(own.DoorAnchor, own.DoorPath)
		if gid == "" {
			return
		}
		g, ok := a.c.Grid(gid)
		if !ok {
			return
		}
		w, ok := g.Tiles[own.TileID]
		if !ok {
			// No row for the doorway, as after a + menu descent, so the
			// level's own root grid owns the framing.
			a.persistFraming(p, nil, "", nil)
			return
		}
		a.persistFraming(p, w, own.DoorAnchor, own.DoorPath)
	}
}

// persistFraming is the one framing writeback: a float center in the grid the
// pane is showing, plus the pane-size-independent intrinsic zoom, onto the
// row that owns it. `door` is the doorway tile the pane entered by, living
// under (doorAnchor, doorPath); nil means a root grid, whose own row owns the
// framing. The zoom is measured against the doorway's footprint, 1x1 for a
// root, so preview and descent agree. A no-op when nothing moved, which is
// measured against what the grid is shown at, never against the stored row:
// see zoomtrans.ShownWellFraming.
func (a *App) persistFraming(p *pane.Pane, door *gridwellv1.Tile, doorAnchor string, doorPath []string) {
	// Never a mid-animation viewport: a pane's centre and zoom are then the
	// transition's scratch values, and storing one would make a frame of an
	// animation the framing the user comes back to. Every writer asks here.
	// A cancelled transition retires before its landing runs, so a write from
	// a landing is the destination.
	if a.trans.Active(p.ID) {
		return
	}
	r := paneRectFor(a, p)
	var (
		req    gridwellv1.SetFramingRequest
		foot   = zoomtrans.Well{W: 1, H: 1}
		cur    rpc.Framing
		gridID string
		commit func(rpc.Framing)
	)
	if door != nil {
		foot = zoomtrans.Well{W: door.W, H: door.H}
		cur = zoomtrans.ShownWellFraming(zoomtrans.WellOf(door))
		gridID = a.gridIDForPathFrom(doorAnchor, doorPath)
		req = gridwellv1.SetFramingRequest{TileId: door.Id}
		commit = func(f rpc.Framing) {
			// cache.Grid hands out the cached rows themselves, so patch a
			// clone through Apply rather than behind it.
			patched := proto.CloneOf(door)
			patched.ViewCx, patched.ViewCy, patched.ViewZoom = f.Cx, f.Cy, f.Zoom
			a.c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{
				TileChanged: &gridwellv1.TileChanged{Tile: patched}}})
		}
	} else {
		if len(p.Path()) > 0 || p.ContentID() != "" {
			return
		}
		pl, ok := a.pluginByRoot(p.Anchor())
		if !ok {
			return
		}
		cur = zoomtrans.ShownRootFraming(
			rpc.Framing{Cx: pl.RootViewCx, Cy: pl.RootViewCy, Zoom: pl.RootViewZoom},
			zoomtrans.OvertakeZoom(foot, r.W, r.H, cellPx))
		gridID = p.Anchor()
		req = gridwellv1.SetFramingRequest{RootGridId: p.Anchor()}
		commit = func(f rpc.Framing) { a.cacheDoorwayFraming(p.Anchor(), f) }
	}
	next := rpc.Framing{Cx: p.Cx, Cy: p.Cy,
		Zoom: zoomtrans.IntrinsicFromLive(p.Zoom, zoomtrans.OvertakeZoom(foot, r.W, r.H, cellPx))}
	if cur.SameAs(next) {
		return
	}
	commit(next)
	req.Cx, req.Cy, req.Zoom = next.Cx, next.Cy, next.Zoom
	// One dispatcher for both rows a framing can live on. They differ only in
	// which id keys the parked write, never in policy.
	key := req.TileId
	if key == "" {
		key = req.RootGridId
	}
	a.postFramingPersist("SetFraming", gridID, key,
		func(ctx context.Context) error {
			_, err := a.cl.SetFraming(ctx, &req)
			return err
		},
		func() (string, []byte, string) {
			path, body := rpc.SetFramingBeacon(&req)
			return path, body, rpc.BeaconJSONType
		})
}

// persistTextScroll is the settle persister's text arm: a text descent's
// scroll persists framing-class, no version bump, one SetTextView when it
// moved. A read-only host tile scrolls like any other, because where the user
// left the window is the node's fact even when the body is the plugin's.
func (a *App) persistTextScroll(p *pane.Pane) {
	file, ok := a.descendedTile(p)
	if !ok || !rpc.TextDocument(file) || a.possiblyEphemeral(p, file) {
		return
	}
	scrollX := int64(p.TextScrollX + 0.5)
	scrollY := int64(p.TextScrollY + 0.5)
	gid := a.gridIDForPane(p)
	r := paneRectFor(a, p)
	_, _, iw, ih := textInnerBox(r)
	next := textedit.Framing{X: scrollX, Y: scrollY, W: int64(iw + 0.5), H: int64(ih + 0.5), Mode: p.TextMode}
	if !textedit.FramingChanged(textedit.FramingOf(file), next) {
		return
	}
	req := &gridwellv1.SetTileRequest{TileId: file.Id,
		Tile: &gridwellv1.Tile{Kind: rpc.KindText,
			TextX: next.X, TextY: next.Y, TextW: next.W, TextH: next.H, TextMode: next.Mode}}
	patched := proto.CloneOf(file)
	patched.TextX, patched.TextY = scrollX, scrollY
	patched.TextW, patched.TextH = next.W, next.H
	patched.TextMode = p.TextMode
	a.c.Apply(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{
		TileChanged: &gridwellv1.TileChanged{Tile: patched}}})
	a.postFramingPersist("SetTextView", gid, file.Id,
		func(ctx context.Context) error {
			_, err := a.cl.SetTile(ctx, req)
			return err
		},
		func() (string, []byte, string) {
			path, body := rpc.SetTileBeacon(req)
			return path, body, rpc.BeaconJSONType
		})
}

// scheduleURLUpdate marks the URL out of date. Cheap to call from any
// state-mutating path.
func (a *App) scheduleURLUpdate() {
	a.persist.sched.urlUpdate.Arm(cadence.URLUpdateMs)
}

// writeURLNow is the one history writer, the DOM half of it. Whether to write
// at all, and push against replace, are the machine's: it diffs this write's
// structural place against the last. Structural navigation pushes an entry so
// back traverses it; framing and focus changes replace in place.
func (a *App) writeURLNow() {
	if !a.nav.URLWritable() {
		return
	}
	state := a.encodeFocusedPaneURL()
	raw := a.withE2EParam(pane.EncodeURL(state))
	var paneID string
	if p := a.tree.FocusedPane(); p != nil {
		paneID = p.ID
	}
	if a.nav.URLWrote(pane.URLPlaceOf(paneID, state)) {
		js.Global().Get("history").Call("pushState", nil, "", raw)
		return
	}
	js.Global().Get("history").Call("replaceState", nil, "", raw)
}

// withE2EParam re-appends the e2e harness gate. pane.EncodeURL rebuilds the
// query from scratch, so without this the first write de-instruments the page
// and a spec that reloads mid-test loses the testhook.
func (a *App) withE2EParam(raw string) string {
	if !strings.Contains(js.Global().Get("location").Get("search").String(), "e2e=1") {
		return raw
	}
	if strings.ContainsRune(raw, '?') {
		return raw + "&e2e=1"
	}
	return raw + "?e2e=1"
}

// encodeFocusedPaneURL projects the focused pane's place into the URL DTO.
// pane.URLStateOf is the projection; the only thing added here is the
// textarea cursor, which is a DOM fact.
func (a *App) encodeFocusedPaneURL() pane.URLState {
	// Inside a pane tile, that tile is the place: the interior is
	// server-owned by the layout blob, so nothing else rides the URL.
	if top := a.ws.Top(); top != nil {
		return pane.URLState{Workspace: top.TileID}
	}
	p := a.tree.FocusedPane()
	if p == nil {
		return pane.URLState{}
	}
	isText := p.ContentID() != "" && p.TextMode == rpc.TextModeText
	var col, row int
	if isText {
		col, row = a.textareaCursorRowCol()
	}
	return pane.URLStateOf(&p.Stack, a.home, isText, col, row)
}

// textareaCursorRowCol returns the textarea cursor as 0-indexed (column,
// row), or (0, 0) when the textarea is not visible.
func (a *App) textareaCursorRowCol() (int, int) {
	if !a.hasTextarea() {
		return 0, 0
	}
	val := a.overlays.textTextarea.Get("value").String()
	off := a.overlays.textTextarea.Get("selectionStart").Int()
	row, col := textcursor.RowColFromOffset(val, off)
	return col, row
}

// applyURLOnBoot restores the place window.location names, the boot arm of
// the one restore verb. Loose on input, so a bookmarked address degrades as
// the canvas changes underneath it.
func (a *App) applyURLOnBoot() {
	a.runGesture(nav.Gesture{Kind: nav.GestureRestore, Raw: locationPath()})
}

// locationPath is what the address bar says, in the form pane.DecodeURL
// reads. The one reader of window.location, so boot and popstate cannot land
// in different places from one address.
func locationPath() string {
	loc := js.Global().Get("location")
	raw := loc.Get("pathname").String()
	if s := loc.Get("search").String(); s != "" {
		raw += s
	}
	return raw
}

// placeCursorAt applies (col, row) to the textarea as a character offset.
// No-op when the textarea is not ready.
func (a *App) placeCursorAt(col, row int) {
	if !a.hasTextarea() {
		return
	}
	val := a.overlays.textTextarea.Get("value").String()
	off := textcursor.OffsetFromRowCol(val, row, col)
	a.overlays.textTextarea.Call("focus")
	a.overlays.textTextarea.Call("setSelectionRange", off, off)
}
