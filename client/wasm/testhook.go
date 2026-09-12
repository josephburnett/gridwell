//go:build js && wasm

package main

import (
	"fmt"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"sort"
	"strings"
	"syscall/js"
	"time"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/anim"
	"github.com/josephburnett/gridwell/client/door"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/markdown"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/pluginhealth"
	"github.com/josephburnett/gridwell/client/wsbar"
)

// window.__gridwellTest, a read-only introspection surface for the Electron
// e2e tests (apps/desktop/e2e). It is compiled into the normal wasm binary
// but installs nothing unless the page carries ?e2e=1. Accessors read
// through the same geometry helpers the input handlers hit-test against, so
// a spec learns where to click while the server's GetGrid stays the
// independent oracle for what was created. shellVisitURL, setTransitionMs and
// setBackstopMs are the three that mutate.
func (a *App) installTestHook() {
	search := js.Global().Get("location").Get("search").String()
	if !strings.Contains(search, "e2e=1") {
		return
	}
	js.Global().Set("__gridwellTest", js.ValueOf(map[string]any{
		"idle":          js.FuncOf(a.thIdle),
		"idleDetail":    js.FuncOf(a.thIdleDetail),
		"origin":        js.FuncOf(a.thOrigin),
		"panes":         js.FuncOf(a.thPanes),
		"previewSigs":   js.FuncOf(a.thPreviewSigs),
		"gridSigs":      js.FuncOf(a.thGridSigs),
		"deadLinks":     js.FuncOf(a.thDeadLinks),
		"transitioning": js.FuncOf(a.thTransitioning),
		"setTransitionMs": js.FuncOf(func(_ js.Value, args []js.Value) any {
			// Stretches the transition clock so a spec can land an event
			// mid-flight.
			if len(args) == 1 {
				totalTransitionMs = args[0].Float()
			}
			return nil
		}),
		"setBackstopMs": js.FuncOf(func(_ js.Value, args []js.Value) any {
			// Retunes the outbox re-post cadence, restarting the wait in
			// flight, so a spec can bound the backstop from both sides
			// without sitting out retry.Backstop.
			if len(args) == 1 {
				a.backstop.Set(time.Duration(args[0].Float()) * time.Millisecond)
			}
			return float64(a.backstop.Duration().Milliseconds())
		}),
		"workspace":     js.FuncOf(a.thWorkspace),
		"bar":           js.FuncOf(a.thBar),
		"plugins":       js.FuncOf(a.thPlugins),
		"palette":       js.FuncOf(a.thPalette),
		"ghost":         js.FuncOf(a.thGhost),
		"cellCenter":    js.FuncOf(a.thCellCenter),
		"shellVisitURL": js.FuncOf(a.thShellVisitURL),
		"localPaneIds":  js.FuncOf(a.thLocalPaneIds),
		"textInnerBox":  js.FuncOf(a.thTextInnerBox),
		"textareaInfo":  js.FuncOf(a.thTextareaInfo),
		"errors":        js.FuncOf(a.thErrors),
		"traces":        js.FuncOf(a.thTraces),
		"shellRenderer": js.FuncOf(a.thShellRenderer),
		"zoomKeyRelays": js.FuncOf(func(js.Value, []js.Value) any { return a.zoomKeyRelays }),
		// Whether a left border-drag resize is armed, polled instead of
		// sleeping between the forwarded press and the canvas half of the
		// drag. Arming is also what parks the live view.
		"leftResizeArmed": js.FuncOf(func(js.Value, []js.Value) any { return a.leftResize != nil }),
		// How many dividers the press grabbed: 1 on a divider's length, 2 at
		// a corner.
		"leftResizeAxes": js.FuncOf(func(js.Value, []js.Value) any {
			if a.leftResize == nil {
				return 0
			}
			return len(a.leftResize.axes)
		}),
		// The armed right-button gesture, "" for none, so a spec can tell
		// "never armed" from "armed but did not commit".
		"rightDragKind": js.FuncOf(func(js.Value, []js.Value) any {
			if a.rightDrag == nil {
				return ""
			}
			return fmt.Sprint(a.rightDrag.kind)
		}),
		// Settle-persist counters: flush passes plus optimistic-persist
		// dispatches by label, so a spec names which stage went quiet
		// instead of timing out on the far-end effect.
		"persistPosts": js.FuncOf(func(js.Value, []js.Value) any {
			out := map[string]any{"framingFlushes": a.persist.framingFlushes}
			for label, n := range a.persist.persistPosts {
				out[label] = n
			}
			return out
		}),
		// The writes the server has not acknowledged, in drain order, as
		// "<op>:<id>".
		"outbox": js.FuncOf(func(js.Value, []js.Value) any {
			out := []any{}
			for _, k := range a.persist.out.Keys() {
				out = append(out, k.Op+":"+k.ID)
			}
			return out
		}),
		"shellStandin": js.FuncOf(a.thShellStandin),
		"shellText":    js.FuncOf(a.thShellText),
		"shellFeed":    js.FuncOf(a.thShellFeed),
		"rawRows":      js.FuncOf(a.thRawRows),
		"shellCellPx": js.FuncOf(func(_ js.Value, args []js.Value) any {
			// Screen center of terminal cell (col, row), 0-based, so a spec
			// can click rendered terminal content at real pixels.
			if len(args) < 2 {
				return nil
			}
			conn := a.shellConnFor(a.tree.Focus)
			if conn == nil || !conn.container.Truthy() {
				return nil
			}
			r := conn.container.Call("getBoundingClientRect")
			cols := conn.term.Get("cols").Float()
			rows := conn.term.Get("rows").Float()
			if cols <= 0 || rows <= 0 {
				return nil
			}
			cw := r.Get("width").Float() / cols
			ch := r.Get("height").Float() / rows
			return map[string]any{
				"x": r.Get("left").Float() + (args[0].Float()+0.5)*cw,
				"y": r.Get("top").Float() + (args[1].Float()+0.5)*ch,
			}
		}),
		"renderedPreviews": js.FuncOf(func(js.Value, []js.Value) any {
			// The rendered-raster cache: tile id to decode state.
			out := map[string]any{}
			for mk, e := range a.views.renderedPrev {
				// The cache keys per (tile, width bucket); the hook
				// aggregates per tile, ready when any bucket decoded.
				id := mk
				if i := strings.IndexByte(mk, 0); i >= 0 {
					id = mk[:i]
				}
				prev, _ := out[id].(map[string]any)
				ready := e.ready && !e.failed
				if prev != nil {
					ready = ready || prev["ready"].(bool)
				}
				out[id] = map[string]any{
					"ready":      ready,
					"failed":     e.failed,
					"panePaints": a.renderedPanePaints[id],
				}
			}
			return out
		}),
	}))
}

// thShellText returns the focused pane's live terminal buffer as text, "" for
// no live shell. The WebGL renderer paints to a canvas and the DOM carries no
// terminal text, so a spec asserting PTY state has to read the buffer API.
func (a *App) thShellText(js.Value, []js.Value) any {
	conn := a.shellConnFor(a.tree.Focus)
	if conn == nil || !conn.term.Truthy() {
		return ""
	}
	buf := conn.term.Get("buffer").Get("active")
	n := buf.Get("length").Int()
	out := ""
	for i := 0; i < n; i++ {
		line := buf.Call("getLine", i)
		if !line.Truthy() {
			continue
		}
		out += line.Call("translateToString", true).String() + "\n"
	}
	return out
}

// thShellFeed writes a raw string into the focused pane's terminal directly,
// not through the PTY. It pins contracts the PTY path re-encodes away: tmux
// paints with a bare LF meaning keep the column, which no shell command can
// deliver, because the inner PTY's ONLCR rewrites LF to CRLF first.
func (a *App) thShellFeed(_ js.Value, args []js.Value) any {
	conn := a.shellConnFor(a.tree.Focus)
	if conn == nil || !conn.term.Truthy() {
		return false
	}
	conn.term.Call("write", args[0].String())
	return true
}

// thShellRenderer returns the focused shell's renderer kind, "" for none. The
// e2e asserts "webgl" so a platform change cannot silently downgrade the
// terminal to the slower DOM fallback.
func (a *App) thShellRenderer(js.Value, []js.Value) any {
	if conn := a.shellConnFor(a.tree.Focus); conn != nil {
		return conn.rendererKind
	}
	return ""
}

// thShellStandin returns the rect a pane's shell snapshot would draw at, the
// same shellStandinRect the renderer uses, or null when nothing is cached.
// args[0] names the pane, defaulting to the focused one.
func (a *App) thShellStandin(_ js.Value, args []js.Value) any {
	p := a.tree.FocusedPane()
	if len(args) > 0 && args[0].Truthy() {
		p = a.tree.FindPane(args[0].String())
	}
	if p == nil || p.ContentID() == "" {
		return nil
	}
	file, ok := a.descendedTile(p)
	if !ok || file.Kind != rpc.KindShell {
		return nil
	}
	// The same box the in-pane draw uses; see render.go's KindShell arm.
	r := a.paneRectByID(p.ID)
	x, y, _, _ := paneContentBox(r)
	cached, ok := a.views.urlPreview.Get(rpc.ContentID(file), file.PreviewBlobId)
	if !ok {
		return nil
	}
	img, ok := previewImage(cached)
	if !ok {
		return nil
	}
	dx, dy, dw, dh, ok := a.shellStandinRect(img, x, y)
	if !ok {
		return nil
	}
	return map[string]any{"x": dx, "y": dy, "w": dw, "h": dh}
}

// thTraces returns the armed ascent-trace highlights as
// [{paneId, tileId, alpha}], alpha from the same anim.FadeAlpha the renderer
// uses.
func (a *App) thTraces(_ js.Value, _ []js.Value) any {
	now := nowMs()
	out := js.Global().Get("Array").New()
	for paneID, tr := range a.traces {
		o := js.Global().Get("Object").New()
		o.Set("paneId", paneID)
		o.Set("tileId", tr.tileID)
		o.Set("alpha", anim.FadeAlpha(now, tr.startMs, traceDurMs))
		out.Call("push", o)
	}
	return out
}

// thTextareaInfo returns the textarea overlay's binding: the pane it covers
// with that pane's inner box, the tile, and whether it has content. nil when
// no pane is in raw-text mode. The split-pane e2e asserts the overlay covers
// only the focused descended pane.
func (a *App) thTextareaInfo(js.Value, []js.Value) any {
	p := a.tree.FocusedPane()
	if p == nil || p.ContentID() == "" || p.TextMode != rpc.TextModeText {
		return nil
	}
	r := paneRectFor(a, p)
	return map[string]any{
		"paneID":     p.ID,
		"tileID":     p.ContentID(),
		"hasContent": a.overlays.textareaReady,
		"x":          r.X,
		"y":          r.Y,
		"w":          r.W,
		"h":          r.H,
	}
}

// thErrors returns the errsurface notice queue, newest first, plus the
// strip's screen geometry so a spec can click a row to dismiss it. It is what
// makes "and no errors surfaced" assertable.
func (a *App) thErrors(js.Value, []js.Value) any {
	notices := a.errs.Notices()
	stripH := errsurface.StripHeight(len(notices))
	rows := make([]any, 0, len(notices))
	for _, n := range notices {
		sev := "error"
		if n.Severity == errsurface.Info {
			sev = "info"
		}
		rows = append(rows, map[string]any{
			"source":   n.Source,
			"message":  n.Message,
			"severity": sev,
			"count":    n.Count,
		})
	}
	return map[string]any{
		"notices":  rows,
		"stripTop": a.height - stripH,
		"stripH":   stripH,
	}
}

// thTextInnerBox returns the focused pane's inner reading box, the same
// textInnerBox the painter lays rendered markdown out to. Empty unless the
// pane is descended into a text tile.
func (a *App) thTextInnerBox(js.Value, []js.Value) any {
	p, r, ok := a.focusedPaneRect()
	if !ok || p.ContentID() == "" {
		return nil
	}
	x, y, w, h := textInnerBox(r)
	return map[string]any{"x": x, "y": y, "w": w, "h": h}
}

// thLocalPaneIds returns the pane ids holding per-pane state, so a spec can
// prove forgetPane tore a collapsed pane's state down rather than orphaning
// it.
func (a *App) thLocalPaneIds(js.Value, []js.Value) any {
	ids := make([]any, 0, len(a.locals))
	for id := range a.locals {
		ids = append(ids, id)
	}
	return ids
}

// thShellVisitURL fires the focused shell's url-click path, what xterm's link
// plugin activate callback does, because a terminal cell cannot be hit-tested
// from the canvas. Mutates state.
func (a *App) thShellVisitURL(_ js.Value, args []js.Value) any {
	if len(args) >= 1 && args[0].Type() == js.TypeString {
		a.shellURLActivate(a.tree.Focus, args[0].String())
	}
	return nil
}

// thIdle reports that no transition, drag, fetch, or tile mutation is in
// flight. Specs poll it instead of sleeping, so they never race the zoom
// animation, the create-then-refetch, or a descent waiting on a row the
// server has not made yet.
func (a *App) thIdle(js.Value, []js.Value) any {
	return !a.trans.Any() &&
		a.tileMutates == 0 &&
		!a.nav.LevelPending() &&
		a.dragging == nil &&
		a.fetch.gridFetch.Len() == 0 &&
		a.fetch.tileFetch.Len() == 0
}

// thIdleDetail names each thIdle component so a stalled wait reports which
// state is stuck instead of timing out bare.
func (a *App) thIdleDetail(js.Value, []js.Value) any {
	grids := make([]any, 0, a.fetch.gridFetch.Len())
	for _, id := range a.fetch.gridFetch.Keys() {
		grids = append(grids, id)
	}
	tiles := make([]any, 0, a.fetch.tileFetch.Len())
	for _, id := range a.fetch.tileFetch.Keys() {
		tiles = append(tiles, id)
	}
	return map[string]any{
		"transition":   a.trans.Any(),
		"tileMutates":  a.tileMutates,
		"levelPending": a.nav.LevelPending(),
		// All three armed gesture states, not just the one thIdle gates on:
		// a spec that sees no ghost needs to know which took the press.
		"dragging":     a.dragging != nil,
		"leftResize":   a.leftResize != nil,
		"rightDrag":    a.rightDrag != nil,
		"gridInflight": grids,
		"tileInflight": tiles,
	}
}

// thOrigin returns the loopback origin the window is served from, so the test
// can reach the same server's Connect-RPC endpoint (the GetGrid oracle).
func (a *App) thOrigin(js.Value, []js.Value) any {
	return js.Global().Get("location").Get("origin").String()
}

// thTransitioning reports whether a pane transition is in flight.
func (a *App) thTransitioning(js.Value, []js.Value) any {
	return a.trans.Any()
}

// thPreviewSigs returns, for the focused pane's leaf grid, a per-tile
// signature of everything the preview renderer reads, including a cached
// well's child grid rows. It reads the cache render reads, so two equal
// captures mean the preview is byte-identical.
func (a *App) thPreviewSigs(js.Value, []js.Value) any {
	p := a.tree.FocusedPane()
	if p == nil {
		return map[string]any{}
	}
	g, ok := a.c.Grid(a.gridIDForPane(p))
	if !ok {
		return map[string]any{}
	}
	out := map[string]any{}
	for id, t := range g.Tiles {
		out[id] = tileSig(t) + a.childSig(t.ChildGridId)
	}
	return out
}

// thGridSigs is thPreviewSigs for an explicit grid id, with no gesture.
// Observing the cache through clicks is self-defeating, because a focus click
// refetches the grid it lands on and heals the divergence a spec wants to
// see.
func (a *App) thGridSigs(_ js.Value, args []js.Value) any {
	if len(args) != 1 {
		return map[string]any{}
	}
	g, ok := a.c.Grid(args[0].String())
	if !ok {
		return map[string]any{}
	}
	out := map[string]any{}
	for id, t := range g.Tiles {
		out[id] = tileSig(t) + a.childSig(t.ChildGridId)
	}
	return out
}

// thDeadLinks reports which of a grid's tiles the client draws dead, links
// into a namespace this node does not declare; see client/deadref. It is the
// one observable of the state, because the face is canvas pixels and the
// absence of an RPC is an absence.
func (a *App) thDeadLinks(_ js.Value, args []js.Value) any {
	if len(args) != 1 {
		return []any{}
	}
	g, ok := a.c.Grid(args[0].String())
	if !ok {
		return []any{}
	}
	ids := []string{}
	for id, t := range g.Tiles {
		tile := t
		if a.deadLink(tile) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

// tileSig flattens one tile row's render-relevant fields.
func tileSig(t *gridwellv1.Tile) string {
	return fmt.Sprintf("v%d k%s @%d,%d %dx%d view%g,%g,%g text%d,%d,%d,%d,%s blob%d prev%d url%q alt%q ref%v",
		t.Version, t.Kind, t.X, t.Y, t.W, t.H,
		t.ViewCx, t.ViewCy, t.ViewZoom,
		t.TextX, t.TextY, t.TextW, t.TextH, t.TextMode,
		t.BlobId, t.PreviewBlobId, t.UrlString, t.AltText, t.Reference)
}

// childSig digests a well's cached child grid, one level, sorted for
// stability. Empty when uncached or not a well.
func (a *App) childSig(childGridID string) string {
	if childGridID == "" {
		return ""
	}
	g, ok := a.c.Grid(childGridID)
	if !ok {
		return "|child:uncached"
	}
	ids := make([]string, 0, len(g.Tiles))
	for id := range g.Tiles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var b strings.Builder
	for _, id := range ids {
		t := g.Tiles[id]
		b.WriteString("|")
		b.WriteString(id)
		b.WriteString(":")
		b.WriteString(tileSig(t))
	}
	return b.String()
}

// thPanes returns one descriptor per live pane.
func (a *App) thPanes(js.Value, []js.Value) any {
	rects := a.layoutPanes()
	out := make([]any, 0, len(rects))
	for id, r := range rects {
		p := a.tree.FindPane(id)
		if p == nil {
			continue
		}
		out = append(out, map[string]any{
			"id":      id,
			"x":       r.X,
			"y":       r.Y,
			"w":       r.W,
			"h":       r.H,
			"focused": id == a.tree.Focus,
			"gridID":  a.gridIDForPane(p),
			"anchor":  p.Anchor(),
			"path":    stringsToAny(p.Path()),
			// The tile this pane is descended into, "" when on a grid.
			"textFocus": p.ContentID(),
			"textMode":  p.TextMode,
			// From the wire stale bit: what the bar's offline chip reads.
			"stale": func() bool {
				g, ok := a.c.Grid(a.gridIDForPane(p))
				return ok && g.Meta.Stale
			}(),
			// Viewport center in grid cells plus zoom, so a spec can drop on
			// a cell it knows is on-screen whatever the stored framing.
			"cx":   p.Cx,
			"cy":   p.Cy,
			"zoom": p.Zoom,
			// Doorways deep: one number, because there is one place stack.
			"placeDepth": p.Depth() - 1,
			"tileIds":    a.paneTileIDs(p),
		})
	}
	return out
}

// paneTileIDs returns the ids of the tiles this pane renders, the cache
// contents render.go iterates. A tile in the GetGrid oracle but absent here is
// the "created, never rendered" bug. Order is unspecified.
func (a *App) paneTileIDs(p *pane.Pane) []any {
	g, ok := a.c.Grid(a.gridIDForPane(p))
	if !ok {
		return []any{}
	}
	ids := make([]any, 0, len(g.Tiles))
	for id := range g.Tiles {
		ids = append(ids, id)
	}
	return ids
}

// thPlugins returns the configured menu rows: identity, the row's own grid
// where it has one, declared collections, pluginhealth class. Empty until
// Handshake lands; the driver polls. A plugin names no grid of its own, so
// rootGridID is empty for one and its collections are where a spec finds a
// grid.
func (a *App) thPlugins(js.Value, []js.Value) any {
	out := make([]any, 0, len(a.plugins))
	for i, pl := range a.plugins {
		entries := make([]any, 0, len(pl.MenuEntries))
		for _, e := range pl.MenuEntries {
			entries = append(entries, map[string]any{
				"id":       e.Id,
				"label":    door.EntryName(pl.Label, e.Label),
				"gridID":   e.GridId,
				"viewCx":   e.ViewCx,
				"viewCy":   e.ViewCy,
				"viewZoom": e.ViewZoom,
			})
		}
		out = append(out, map[string]any{
			"index":         i,
			"kind":          pl.Kind,
			"label":         pl.Label,
			"uuid":          pl.Uuid,
			"rootGridID":    pl.RootGridId,
			"menuEntries":   entries,
			"scratchGridID": pl.ScratchGridId,
			"infoError":     pl.InfoError,
			"status":        pluginStatusName(pl),
			"rootViewCx":    pl.RootViewCx,
			"rootViewCy":    pl.RootViewCy,
			"rootViewZoom":  pl.RootViewZoom,
		})
	}
	return out
}

// pluginStatusName is the stable string for a plugin's pluginhealth class,
// shared by thPlugins/thPalette.
func pluginStatusName(pl *gridwellv1.PluginInfo) string {
	switch pluginhealth.Classify(pl) {
	case pluginhealth.Broken:
		return "broken"
	case pluginhealth.Waiting:
		return "waiting"
	case pluginhealth.NoDoor:
		return "nodoor"
	}
	return "enterable"
}

// thPalette returns the creation palette for the focused pane. The rects are
// the ones paletteTileIndexAt hit-tests, so a click at an entry's center lands
// on that swatch.
func (a *App) thPalette(js.Value, []js.Value) any {
	p, _, ok := a.focusedPaneRect()
	if !ok {
		return map[string]any{"open": false}
	}
	px, py := a.plusButtonCenter()
	l, show := a.paletteLayoutAndShow(p)
	items := a.paletteItems(p)
	entries := make([]any, 0, len(items))
	for i, item := range items {
		tx, ty, tw, th := a.paletteTileRect(p, i)
		e := map[string]any{
			"index":    i,
			"isPlugin": item.isPlugin,
			"x":        tx,
			"y":        ty,
			"w":        tw,
			"h":        th,
		}
		if item.isPlugin {
			e["kind"] = item.plugin.Kind
			e["label"] = item.plugin.Label
			e["uuid"] = item.plugin.Uuid
			e["rootGridID"] = item.plugin.RootGridId
			e["status"] = pluginStatusName(item.plugin)
			// The exact selector drawPaletteItem renders.
			e["glyph"] = door.RowGlyph(item.plugin)
		} else {
			e["kind"] = templateKindName(item.primitive)
		}
		// A declared menu entry reports its identity, but the label stays
		// door.EntryName's, the one the banner draws: a second name for
		// one doorway would let a spec pin the wrong one.
		if item.entry != nil {
			e["entry"] = item.entry.Id
		}
		entries = append(entries, e)
	}
	tr := l.ToggleRect()
	return map[string]any{
		"open":  a.menu.OpenOn(p.ID),
		"plusX": px,
		"plusY": py,
		"items": entries,
		// The plugin section's disclosure strip. The rect is the one
		// pointInPaletteToggle hit-tests, so a click at its center works the
		// control.
		"toggle": map[string]any{
			"present":  show.Toggle,
			"chevron":  show.Chevron.String(),
			"expanded": show.Plugins,
			"x":        tr.X,
			"y":        tr.Y,
			"w":        tr.W,
			"h":        tr.H,
		},
		// The hovered swatch index, -1 for none, straight off client/menu.
		"hover": a.menu.Hover(),
	}
}

// thGhost reports the drag ghost the renderer would paint and the source tile
// it hides, the two facts the tile loop reads at its hide check.
func (a *App) thGhost(js.Value, []js.Value) any {
	return map[string]any{
		"active":       a.ghost != nil,
		"hiddenTileID": a.ghostHiddenTile(),
		"hiddenPaneID": a.ghostHiddenPane(),
	}
}

// thCellCenter maps a grid cell (cx, cy) in the named pane to its screen-space
// center, so a test can target a drop on an exact cell. args: paneID, cx, cy.
func (a *App) thCellCenter(_ js.Value, args []js.Value) any {
	if len(args) < 3 {
		return nil
	}
	p := a.tree.FindPane(args[0].String())
	rects := a.layoutPanes()
	r, ok := rects[args[0].String()]
	if p == nil || !ok {
		return nil
	}
	cx, cy := args[1].Float(), args[2].Float()
	sx, sy := paneToDragdrop(p, r).CellToScreen(cx+0.5, cy+0.5)
	return map[string]any{"x": sx, "y": sy}
}

func (a *App) focusedPaneRect() (*pane.Pane, pane.Rect, bool) {
	p := a.tree.FindPane(a.tree.Focus)
	if p == nil {
		return nil, pane.Rect{}, false
	}
	r, ok := a.layoutPanes()[a.tree.Focus]
	return p, r, ok
}

// templateKindName reads the primitives table, so the name a spec picks a
// swatch by cannot drift from the swatch.
func templateKindName(k templateKind) string {
	if pr, ok := primitiveFor(k); ok {
		return pr.name
	}
	return ""
}

// stringsToAny lifts a []string to the []any js.ValueOf needs for a JS array.
func stringsToAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// thBar exposes the one bottom bar: its rectangle, whose left and width are
// the focused pane's span because the bar rides it, and every segment's rect
// and identity. Read-only over the layout drawBottomBar renders and
// bottomBarClick hit-tests, so a spec's click at a segment center is the click
// the user would make.
func (a *App) thBar(js.Value, []js.Value) any {
	bx, top, bw, ok := a.bottomBarRect()
	if !ok {
		return map[string]any{"top": 0.0, "height": wsbar.RowH, "segments": []any{}}
	}
	chain := a.navChain()
	segs := a.bottomBarSegments(chain)
	out := make([]any, 0, len(segs))
	for _, s := range segs {
		// X is absolute so specs click hook coordinates verbatim. Index
		// addresses the full chain, which left-truncation may not all show.
		e := map[string]any{
			"x": bx + s.X, "w": s.W, "index": s.Index,
		}
		nc := chain[s.Index]
		if nc.PaneTile {
			e["kind"] = "pane"
			e["level"] = nc.WsLevel
			e["tileID"] = nc.TileID
		} else {
			e["kind"] = "chain"
			e["level"] = a.ws.Depth() // chain crumbs are the live tree's
			e["anchor"] = nc.Crumb.Anchor
			e["tileID"] = nc.Crumb.TileID
			e["text"] = nc.Crumb.Text
			// The leading close-all crumb stands for the levels, not the
			// pane's own chain.
			e["closeOnly"] = nc.CloseOnly
			if nc.Crumb.Anchor != "" {
				// The exact selector drawChainCrumb renders.
				e["glyph"] = a.pluginGlyph(nc.Crumb.Anchor)
			}
		}
		out = append(out, e)
	}
	band, button := a.barTheme()
	res := map[string]any{
		"top":      top,
		"left":     bx,
		"width":    bw,
		"height":   wsbar.RowH,
		"band":     band,
		"button":   button,
		"segments": out,
	}
	// The centered title: the rect drawBarTitle renders and bottomBarClick
	// hit-tests.
	if x, w, label, editable, muted, ok := a.barTitleGeom(); ok {
		res["title"] = map[string]any{
			"x": x, "w": w, "label": label, "editable": editable, "muted": muted,
		}
	}
	return res
}

// thRawRows returns how many visual rows the canvas painter would produce for
// the focused text descent's textarea content: the wrap-parity oracle a spec
// compares against the textarea's own scrollHeight-derived row count.
func (a *App) thRawRows(js.Value, []js.Value) any {
	p := a.tree.FocusedPane()
	if p == nil || p.ContentID() == "" || !a.overlays.textTextarea.Truthy() {
		return -1
	}
	src := a.overlays.textTextarea.Get("value").String()
	st := defaultMarkdownStyle()
	scale := a.textScaleFor(p)
	setFont(a.cctx, st.codePx*scale, st.monospace, false)
	m := a.cctx.Call("measureText", "M")
	w := a.textContentWidth(p)
	return len(markdown.WrapRawText(src, rawWrapCols(m, w, scale, st.pad)))
}

// thWorkspace exposes the workspace stack: depth, crumb names, and the
// current pane tile's id. Read-only over the state the bar renders.
func (a *App) thWorkspace(js.Value, []js.Value) any {
	names := a.ws.Names()
	out := map[string]any{
		"depth": a.ws.Depth(),
		"names": stringsToAny(names),
	}
	if top := a.ws.Top(); top != nil {
		out["tileID"] = top.TileID
		out["readOnly"] = top.ReadOnly
	}
	return out
}
