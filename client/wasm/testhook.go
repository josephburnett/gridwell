//go:build js && wasm

package main

import (
	"fmt"
	"sort"
	"strings"
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/anim"
	"github.com/josephburnett/gridwell/client/door"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/markdown"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/pluginhealth"
	"github.com/josephburnett/gridwell/client/wsbar"
)

// This file exposes a read-only introspection surface, window.__gridwellTest,
// used exclusively by the Electron end-to-end tests (apps/desktop/e2e). It is
// compiled into the normal wasm binary but installs nothing unless the page was
// loaded with ?e2e=1, so production never sees it.
//
// Almost every accessor is a pure read over state the app already holds,
// reusing the same geometry helpers the input handlers hit-test against
// (plusButtonCenter, paletteTileRect, paneToDragdrop). It lets a test learn
// where to click and when the world has settled, while the server's GetGrid
// remains the independent oracle for what was created. The one exception is
// shellVisitURL, an e2e-only action that fires the exact callback xterm's
// link provider invokes on a click: a terminal-cell link click cannot be
// hit-tested from the canvas, so this drives it directly, mirroring the
// desktop's __gwRegistry e2e seam. Both are gated to ?e2e=1.

// installTestHook wires window.__gridwellTest when the page carries ?e2e=1.
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
			// An e2e-only action, like shellVisitURL: stretch the transition
			// clock so a spec can deterministically land an event
			// mid-flight.
			if len(args) == 1 {
				totalTransitionMs = args[0].Float()
			}
			return nil
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
		// leftResizeArmed reports whether a left border-drag resize is
		// armed: the observable a spec polls instead of sleeping between
		// the forwarded press and the canvas half of the drag. Arming is
		// also what parks the live view.
		"leftResizeArmed": js.FuncOf(func(js.Value, []js.Value) any { return a.leftResize != nil }),
		// leftResizeAxes is how many dividers the press grabbed: 1 on a
		// divider's length, 2 at a corner. A spec that drags a corner needs to
		// distinguish "grabbed both" from "grabbed one and the other split
		// happened to move".
		"leftResizeAxes": js.FuncOf(func(js.Value, []js.Value) any {
			if a.leftResize == nil {
				return 0
			}
			return len(a.leftResize.axes)
		}),
		// rightDragKind names the armed right-button gesture, "" for none:
		// which gesture a right-down classified into, so a spec can
		// distinguish "never armed" from "armed but did not commit".
		"rightDragKind": js.FuncOf(func(js.Value, []js.Value) any {
			if a.rightDrag == nil {
				return ""
			}
			return fmt.Sprint(a.rightDrag.kind)
		}),
		// persistPosts exposes the settle-persist observability counters:
		// flush passes plus optimistic-persist dispatches by label. A spec
		// can name which stage of gesture, debounce, flush, post went quiet
		// instead of timing out on the far-end effect.
		"persistPosts": js.FuncOf(func(js.Value, []js.Value) any {
			out := map[string]any{"framingFlushes": a.persist.framingFlushes}
			for label, n := range a.persist.persistPosts {
				out[label] = n
			}
			return out
		}),
		// outbox lists the writes the server has not acknowledged, in drain
		// order, as "<op>:<id>": the observable behind "did that outage
		// park my work, and did the reconnect land it?".
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
			// can click rendered terminal content (links) at real pixels.
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
			// The rendered-raster cache: tile id to decode state. A spec can
			// prove a rendered-mode tile's preview switched to the
			// rasterized path, and that the raster decoded.
			out := map[string]any{}
			for mk, e := range a.views.renderedPrev {
				// The cache keys per (tile, width bucket); the hook
				// aggregates per tile: ready when any bucket's raster
				// decoded.
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

// thShellText returns the focused pane's live terminal buffer as text: every
// line, trimmed of trailing blanks. The WebGL renderer paints to a canvas and
// the DOM carries no terminal text, so specs that assert PTY state read it
// through the buffer API, the same read the link provider uses per line. ""
// means no live shell.
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

// thShellFeed writes a raw string into the focused pane's live terminal
// directly, not through the PTY. It pins terminal-level contracts the PTY
// path re-encodes away: tmux paints with a bare LF as a keep-the-column
// index, and convertEol would snap it to column 0 — unreachable through a
// shell command, whose LFs the inner PTY's ONLCR rewrites to CRLF before tmux
// re-encodes them.
func (a *App) thShellFeed(_ js.Value, args []js.Value) any {
	conn := a.shellConnFor(a.tree.Focus)
	if conn == nil || !conn.term.Truthy() {
		return false
	}
	conn.term.Call("write", args[0].String())
	return true
}

// thShellRenderer returns which renderer the focused pane's live shell
// attached: "webgl" or "canvas", with "" for no live shell. The e2e asserts
// "webgl", so a platform change cannot silently downgrade the terminal
// renderer and bring back the rendering artifacts the canvas renderer has.
func (a *App) thShellRenderer(js.Value, []js.Value) any {
	if conn := a.shellConnFor(a.tree.Focus); conn != nil {
		return conn.rendererKind
	}
	return ""
}

// thShellStandin returns the rect a pane's shell snapshot would draw at right
// now — the same shellStandinRect the renderer uses — or null when no cached
// preview exists. args[0] names the pane, defaulting to the focused one. A
// spec can assert the parked stand-in sits exactly where the live xterm
// canvas was.
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
	// The same box the in-pane draw uses (render.go's KindShell arm).
	r := a.paneRectByID(p.ID)
	x, y, _, _ := paneContentBox(r)
	cached, ok := a.views.urlPreview.Get(file.ContentID(), file.PreviewBlobID)
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
// [{paneId, tileId, alpha}]: the fading "you just came from here" outlines. A
// pure read; alpha is computed with the same FadeAlpha the renderer uses, so
// a spec can watch a trace arm and then expire.
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

// thTextareaInfo returns the current textarea overlay's binding: which pane it
// covers, which tile it's bound to, and whether it currently has content
// (textareaReady). Returns nil when no pane is in raw-text mode.
//
// Used by the split-pane text-tile e2e to assert the overlay covers only the
// focused descended pane, never a preview node in another pane.
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
		// The focused pane's inner box — lets e2e verify the overlay is over the
		// right pane's geometry, not a sibling.
		"x": r.X,
		"y": r.Y,
		"w": r.W,
		"h": r.H,
	}
}

// thErrors returns the errsurface notice queue, newest first, as
// [{source, message, severity, count}], plus the strip's screen geometry so
// an e2e can click a row to dismiss it. A read-only view of the one error
// owner, and what makes failures assertable: a spec can end with "and no
// errors surfaced".
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

// thTextInnerBox returns the focused pane's inner reading box — the rect
// rendered markdown is laid out and clipped to — as {x, y, w, h} in screen
// pixels. It is the same textInnerBox the painter uses, so an e2e can click a
// known position inside the rendered text. Empty when the focused pane is not
// descended into a text tile.
func (a *App) thTextInnerBox(js.Value, []js.Value) any {
	p, r, ok := a.focusedPaneRect()
	if !ok || p.ContentID() == "" {
		return nil
	}
	x, y, w, h := textInnerBox(r)
	return map[string]any{"x": x, "y": y, "w": w, "h": h}
}

// thLocalPaneIds returns the pane ids that currently hold per-pane state in
// a.locals. Lets an e2e prove forgetPane: after a pane is collapsed, its id must
// be gone here (its per-pane state was torn down, not orphaned).
func (a *App) thLocalPaneIds(js.Value, []js.Value) any {
	ids := make([]any, 0, len(a.locals))
	for id := range a.locals {
		ids = append(ids, id)
	}
	return ids
}

// thShellVisitURL fires the focused shell's url-click path (what xterm's link
// plugin activate callback does) so an e2e can exercise the shell→ephemeral-
// url descent without hit-testing a terminal cell. e2e-only; mutates state.
func (a *App) thShellVisitURL(_ js.Value, args []js.Value) any {
	if len(args) >= 1 && args[0].Type() == js.TypeString {
		a.shellURLActivate(a.tree.Focus, args[0].String())
	}
	return nil
}

// thIdle reports true when no transition, drag, fetch, or tile mutation is in
// flight — i.e. the descent/ascent animation has finished, every pending
// GetGrid/GetTile has resolved, AND no Create* is still out. Tests poll this
// instead of sleeping, so they never race the ~350ms zoom animation, the async
// create→fetchGrid refresh, or a gesture whose descent waits on a row the
// server has not made yet.
func (a *App) thIdle(js.Value, []js.Value) any {
	return !a.trans.Any() &&
		a.tileMutates == 0 &&
		!a.nav.LevelPending() &&
		a.dragging == nil &&
		a.fetch.gridFetch.Len() == 0 &&
		a.fetch.tileFetch.Len() == 0
}

// thIdleDetail names each idle() component so a stalled waitIdle in a spec
// reports WHICH state is stuck (a hung fetch reads very differently from a
// stuck transition) instead of a bare timeout.
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
		// All three armed gesture states, not just the one idle() gates on:
		// a press that arms the wrong gesture and a press that arms nothing
		// read identically from "dragging" alone, and a spec that sees no
		// ghost needs to know which state took the press.
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

// thPanes returns one descriptor per live pane: its screen rect, whether it is
// focused, and the qualified grid id / anchor / path it currently frames.
// thTransitioning reports whether a pane transition (descent/ascent
// animation) is in flight — the window I11's injection spec aims for.
func (a *App) thTransitioning(js.Value, []js.Value) any {
	return a.trans.Any()
}

// thPreviewSigs returns, for the focused pane's leaf grid, a per-tile
// signature of everything the preview renderer reads: the tile row's content
// identity and framing fields, and, for a well whose child grid is cached,
// the child grid's tile rows too, since a well's preview is its child grid
// drawn small. It reads the same cache render reads, so the signature cannot
// disagree with pixels. Two equal captures mean the preview is
// byte-identical.
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
		out[id] = tileSig(&t) + a.childSig(t.ChildGridID)
	}
	return out
}

// thGridSigs is previewSigs for an explicit grid id: the same per-tile
// signatures, read straight from the cache with no gesture. Observing the
// cache through clicks is self-defeating, because a focus click refetches the
// grid it lands on and heals exactly the divergence a spec wants to see.
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
		out[id] = tileSig(&t) + a.childSig(t.ChildGridID)
	}
	return out
}

// thDeadLinks reports which tiles of a grid the client draws dead: links
// into a namespace this node does not declare (client/deadref). It is the
// one observable of the state — the face is canvas pixels and the absence of
// an RPC is an absence — so a spec can pin that the verdict crossed the seam
// from server.yaml to the tile.
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
		if a.deadLink(&tile) {
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
func tileSig(t *rpc.Tile) string {
	return fmt.Sprintf("v%d k%s @%d,%d %dx%d view%g,%g,%g text%d,%d,%d,%d,%s blob%d prev%d url%q alt%q ref%v",
		t.Version, t.Kind, t.X, t.Y, t.W, t.H,
		t.ViewCx, t.ViewCy, t.ViewZoom,
		t.TextX, t.TextY, t.TextW, t.TextH, t.TextMode,
		t.BlobID, t.PreviewBlobID, t.URLString, t.AltText, t.Reference)
}

// childSig digests a well's cached child grid (one level — what the preview
// draws), sorted for stability. Empty when uncached or not a well.
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
		b.WriteString(tileSig(&t))
	}
	return b.String()
}

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
			// The tile this pane is descended into, "" when on a grid, so a
			// test can tell a shell descent from the url it descended
			// further into.
			"textFocus": p.ContentID(),
			// The descent's live raw or rendered mode.
			"textMode": p.TextMode,
			// The pane's grid is a cache-served memory, from the wire stale
			// bit: what the bar's offline chip reads.
			"stale": func() bool {
				g, ok := a.c.Grid(a.gridIDForPane(p))
				return ok && g.Meta.Stale
			}(),
			// Viewport center (grid cell coords) + zoom, so a test can drop on a
			// cell guaranteed to be on-screen regardless of the stored framing.
			"cx":   p.Cx,
			"cy":   p.Cy,
			"zoom": p.Zoom,
			// How many doorways deep the pane is: one number, because there
			// is one place stack. A spec asserts that no flow leaks a
			// frame.
			"placeDepth": p.Depth() - 1,
			// The ids of the tiles this pane renders: its cache contents.
			// The gap between this and the server, the GetGrid oracle, is
			// the create-cache-render and subscribe-fanout seam where a
			// tile disappears — present on the server but never drawn.
			"tileIds": a.paneTileIDs(p),
		})
	}
	return out
}

// paneTileIDs returns the ids of the tiles this pane currently RENDERS — the
// cache contents for the pane's grid, which render.go iterates to draw. An e2e
// uses it to assert a created tile is actually drawn, not merely present on the
// server: a tile in the GetGrid oracle but absent here is the "it disappeared"
// bug (created, never rendered). Order is unspecified (map iteration).
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

// thPlugins returns the configured plugin list (identity, root/scratch grids,
// pluginhealth classification) with no screen positions — available wherever
// the pane sits. Empty until Handshake lands; the driver polls.
func (a *App) thPlugins(js.Value, []js.Value) any {
	out := make([]any, 0, len(a.plugins))
	for i, pl := range a.plugins {
		out = append(out, map[string]any{
			"index":         i,
			"kind":          pl.Kind,
			"label":         pl.Label,
			"uuid":          pl.UUID,
			"rootGridID":    pl.RootGridID,
			"scratchGridID": pl.ScratchGridID,
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
func pluginStatusName(pl rpc.PluginInfo) string {
	switch pluginhealth.Classify(pl) {
	case pluginhealth.Broken:
		return "broken"
	case pluginhealth.Waiting:
		return "waiting"
	}
	return "enterable"
}

// thPalette returns the creation palette for the focused pane: whether it is
// open, the + button center, and one entry per swatch with its screen rect and
// identity. The rects are the same ones paletteTileIndexAt hit-tests, so a click
// at an entry's center lands on that swatch.
func (a *App) thPalette(js.Value, []js.Value) any {
	p, _, ok := a.focusedPaneRect()
	if !ok {
		return map[string]any{"open": false}
	}
	px, py := a.plusButtonCenter()
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
			e["uuid"] = item.plugin.UUID
			e["rootGridID"] = item.plugin.RootGridID
			e["status"] = pluginStatusName(item.plugin)
			// The swatch's face, the exact selector drawPaletteItem renders,
			// so a spec can pin it against the crumb of the grid this row
			// roots without reading pixels.
			e["glyph"] = door.RowGlyph(item.plugin)
		} else {
			e["kind"] = templateKindName(item.primitive)
		}
		// A plugin-declared root entry reports its identity so a
		// test can tell it from the declaring plugin's own row.
		if item.entry != nil {
			e["entry"] = item.entry.ID
			e["label"] = item.entry.Label
		}
		entries = append(entries, e)
	}
	return map[string]any{
		"open":  a.menu.OpenOn(p.ID),
		"plusX": px,
		"plusY": py,
		"items": entries,
		// The hovered swatch index, -1 for none: what the palette highlights,
		// straight off client/menu, the one owner. A spec asserts the hover
		// routed to the menu's pane without reading pixels.
		"hover": a.menu.Hover(),
	}
}

// thGhost reports the drag ghost the renderer would paint this frame and the
// source tile it hides while it stands in for it — the same two facts the tile
// loop reads at its hide check, so "the source tile draws again" is assertable
// without reading pixels. A hidden tile with no way to un-hide it is what a
// lost release used to leave behind.
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

// focusedPaneRect returns the focused pane and its laid-out screen rect.
func (a *App) focusedPaneRect() (*pane.Pane, pane.Rect, bool) {
	p := a.tree.FindPane(a.tree.Focus)
	if p == nil {
		return nil, pane.Rect{}, false
	}
	r, ok := a.layoutPanes()[a.tree.Focus]
	return p, r, ok
}

// templateKindName is the stable string a test uses to pick a primitive
// swatch, read from the primitives table so a name cannot drift from the
// swatch it names.
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

// thBar exposes the one bottom bar: its drawn rectangle — top, left and width,
// the last two being the focused pane's span, since the bar rides it — and
// every segment's rect and identity: level crumbs by level, chain crumbs by
// index with the tile or anchor they stand for. It always shows the focused
// pane, so there is nothing to address it by. Read-only over the exact layout
// drawBottomBar renders and bottomBarClick hit-tests, so a spec's click at a
// segment center is the click the user would make.
func (a *App) thBar(js.Value, []js.Value) any {
	bx, top, bw, ok := a.bottomBarRect()
	if !ok {
		return map[string]any{"top": 0.0, "height": wsbar.RowH, "segments": []any{}}
	}
	chain := a.navChain()
	segs := a.bottomBarSegments(chain)
	out := make([]any, 0, len(segs))
	for _, s := range segs {
		// Segment X is emitted absolute so specs click hook coordinates
		// verbatim. Index addresses the full chain, and left-truncation
		// drops leading crumbs.
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
			// The leading close-all crumb is not one of the focused pane's
			// own: it stands for the levels, and its click closes them.
			e["closeOnly"] = nc.CloseOnly
			if nc.Crumb.Anchor != "" {
				// A root crumb's face: the exact selector drawChainCrumb
				// renders, so a spec can pin crumb identity without reading
				// pixels.
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
	// The centered current-pane title: the exact rect drawBarTitle renders
	// and bottomBarClick hit-tests.
	if x, w, label, editable, muted, ok := a.barTitleGeom(); ok {
		res["title"] = map[string]any{
			"x": x, "w": w, "label": label, "editable": editable, "muted": muted,
		}
	}
	return res
}

// thRawRows returns how many visual rows the canvas painter would produce for
// the focused text descent's current textarea content: the wrap-parity
// oracle. A spec compares it against the textarea's own scrollHeight-derived
// row count, crossing the browser-soft-wrap against canvas-wrap seam with the
// same bytes on both sides.
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

// thWorkspace exposes the workspace stack: nesting depth, crumb names, and
// the current pane tile's id — what a spec needs to assert "the bar is
// there, named right, and ascent actually left". Read-only over the same
// state the bar renders, so it can't disagree with pixels.
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
