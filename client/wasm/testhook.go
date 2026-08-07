//go:build js && wasm

package main

import (
	"fmt"
	"sort"
	"strings"
	"syscall/js"

	"github.com/josephburnett/gridwell/client/anim"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/markdown"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/pluginhealth"
	"github.com/josephburnett/gridwell/client/wsbar"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// This file exposes a read-only introspection surface, window.__gridwellTest,
// used exclusively by the Electron end-to-end tests (apps/desktop/e2e). It is
// compiled into the normal wasm binary but installs nothing unless the page was
// loaded with ?e2e=1, so production never sees it.
//
// Almost every accessor is a pure READ over state the app already holds,
// reusing the same geometry helpers the input handlers hit-test against
// (plusButtonCenter, paletteTileRect, paneToDragdrop) — it just lets a test
// learn where to click and when the world has settled, while the server's
// GetGrid remains the independent oracle for what was created. The lone
// exception is shellVisitURL, an e2e-only ACTION that fires the exact callback
// xterm's link provider invokes on a click — a terminal-cell link click can't
// be hit-tested from the canvas, so this drives it directly, mirroring the
// desktop's __gwRegistry e2e seam. Both are gated to ?e2e=1, so production
// never sees them.

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
		"transitioning": js.FuncOf(a.thTransitioning),
		"setTransitionMs": js.FuncOf(func(_ js.Value, args []js.Value) any {
			// e2e-only ACTION (like shellVisitURL): stretch the transition
			// clock so a spec can deterministically land an event mid-flight.
			if len(args) == 1 {
				totalTransitionMs = args[0].Float()
			}
			return nil
		}),
		"workspace":     js.FuncOf(a.thWorkspace),
		"bar":           js.FuncOf(a.thBar),
		"launcher":      js.FuncOf(a.thLauncher),
		"plugins":       js.FuncOf(a.thPlugins),
		"nodeGrid":      js.FuncOf(func(js.Value, []js.Value) any { return a.nodeGrid }),
		"palette":       js.FuncOf(a.thPalette),
		"cellCenter":    js.FuncOf(a.thCellCenter),
		"shellVisitURL": js.FuncOf(a.thShellVisitURL),
		"localPaneIds":  js.FuncOf(a.thLocalPaneIds),
		"textInnerBox":  js.FuncOf(a.thTextInnerBox),
		"textareaInfo":  js.FuncOf(a.thTextareaInfo),
		"errors":        js.FuncOf(a.thErrors),
		"traces":        js.FuncOf(a.thTraces),
		"shellRenderer": js.FuncOf(a.thShellRenderer),
		"zoomKeyRelays": js.FuncOf(func(js.Value, []js.Value) any { return a.zoomKeyRelays }),
		"shellStandin":  js.FuncOf(a.thShellStandin),
		"shellText":     js.FuncOf(a.thShellText),
		"shellFeed":     js.FuncOf(a.thShellFeed),
		"rawRows":       js.FuncOf(a.thRawRows),
		"shellCellPx": js.FuncOf(func(_ js.Value, args []js.Value) any {
			// Screen center of terminal cell (col, row), 0-based — lets a
			// spec CLICK rendered terminal content (links) at real pixels.
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
			// The rendered-raster cache (issue #233): tile id → decode state.
			// Lets a spec prove a rendered-mode tile's preview switched to
			// the rasterized path (and the raster actually decoded).
			out := map[string]any{}
			for id, e := range a.renderedPrev {
				out[id] = map[string]any{"ready": e.ready, "failed": e.failed}
			}
			return out
		}),
	}))
}

// thShellText returns the focused pane's live terminal buffer as text
// (every line, trimmed of trailing blanks). The WebGL renderer paints to a
// canvas — the DOM carries no terminal text — so specs that assert PTY
// state (issue #202's same-session reconnect) read it through the buffer
// API, the same read the link provider uses per line. "" = no live shell.
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

// thShellFeed writes a raw string into the focused pane's live terminal —
// directly, NOT through the PTY. It exists to pin terminal-level contracts
// the PTY path re-encodes away (issue #211: tmux paints with bare LF as a
// keep-the-column index, and convertEol silently snapped it to column 0 —
// unreachable through a shell command, whose LFs the inner PTY's ONLCR
// rewrites to CRLF before tmux ever re-encodes them).
func (a *App) thShellFeed(_ js.Value, args []js.Value) any {
	conn := a.shellConnFor(a.tree.Focus)
	if conn == nil || !conn.term.Truthy() {
		return false
	}
	conn.term.Call("write", args[0].String())
	return true
}

// thShellRenderer returns which renderer the focused pane's live shell
// attached ("webgl" / "canvas"; "" = no live shell). The e2e asserts "webgl"
// so a platform change can never silently downgrade the terminal renderer
// again (issue #128 — Chromium dropped the automatic SwiftShader fallback
// and the #84 artifact class returned unnoticed).
func (a *App) thShellRenderer(js.Value, []js.Value) any {
	if conn := a.shellConnFor(a.tree.Focus); conn != nil {
		return conn.rendererKind
	}
	return ""
}

// thShellStandin returns the rect a pane's shell snapshot would draw at
// right now — the SAME shellStandinRect the renderer uses (issue #224) —
// or null when no cached preview exists. args[0] names the pane (default:
// the focused pane). Lets a spec assert the parked stand-in sits exactly
// where the live xterm canvas was.
func (a *App) thShellStandin(_ js.Value, args []js.Value) any {
	p := a.tree.FocusedPane()
	if len(args) > 0 && args[0].Truthy() {
		p = a.tree.FindPane(args[0].String())
	}
	if p == nil || p.TextFocus == "" {
		return nil
	}
	file, ok := a.descendedTile(p)
	if !ok || file.Kind != rpc.KindShell {
		return nil
	}
	// The same box the in-pane draw uses (render.go's KindShell arm).
	r := a.paneRectByID(p.ID)
	x, y, _, _ := paneContentBox(r)
	cached, ok := a.urlPreview.Get(file.ContentID(), file.PreviewBlobID)
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
// [{paneId, tileId, alpha}] — the fading "you just came from HERE" outlines
// (issue #83). Pure read; alpha is computed with the same FadeAlpha the
// renderer uses, so a spec can watch a trace arm and then expire.
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
// focused descended pane and not preview nodes in other panes — the structural
// invariant that issue #35 mechanism B violated.
func (a *App) thTextareaInfo(js.Value, []js.Value) any {
	p := a.tree.FocusedPane()
	if p == nil || p.TextFocus == "" || p.TextMode != rpc.TextModeText {
		return nil
	}
	r := paneRectFor(a, p)
	return map[string]any{
		"paneID":     p.ID,
		"tileID":     p.TextFocus,
		"hasContent": a.textareaReady,
		// The focused pane's inner box — lets e2e verify the overlay is over the
		// right pane's geometry, not a sibling.
		"x": r.X,
		"y": r.Y,
		"w": r.W,
		"h": r.H,
	}
}

// thErrors returns the errsurface notice queue (newest first) as
// [{source, message, severity, count}], plus the strip's screen geometry so
// an e2e can click a row to dismiss it. Read-only view of the one error
// owner — this is also what makes failures assertable in specs instead of
// invisible (charter §6): any spec can now end with "and no errors surfaced".
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

// thTextInnerBox returns the focused pane's file inner reading box (the rect
// rendered markdown is laid out and clipped to) as {x, y, w, h} in screen
// pixels — the same textInnerBox the painter uses, so an
// e2e can click a known position inside the rendered text. Empty when the
// focused pane is not descended into a file.
func (a *App) thTextInnerBox(js.Value, []js.Value) any {
	p, r, ok := a.focusedPaneRect()
	if !ok || p.TextFocus == "" {
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
// provider activate callback does) so an e2e can exercise the shell→ephemeral-
// url descent without hit-testing a terminal cell. e2e-only; mutates state.
func (a *App) thShellVisitURL(_ js.Value, args []js.Value) any {
	if len(args) >= 1 && args[0].Type() == js.TypeString {
		a.shellURLActivate(a.tree.Focus, args[0].String())
	}
	return nil
}

// thIdle reports true when no transition, drag, or fetch is in flight — i.e. the
// descent/ascent animation has finished AND every pending GetGrid/GetTile has
// resolved. Tests poll this instead of sleeping, so they never race the ~350ms
// zoom animation or the async create→fetchGrid refresh.
func (a *App) thIdle(js.Value, []js.Value) any {
	return a.transition == nil &&
		a.wsPending == nil &&
		a.dragging == nil &&
		len(a.gridInflight) == 0 &&
		len(a.tileInflight) == 0
}

// thIdleDetail names each idle() component so a stalled waitIdle in a spec
// reports WHICH state is stuck (a hung fetch reads very differently from a
// stuck transition) instead of a bare timeout.
func (a *App) thIdleDetail(js.Value, []js.Value) any {
	grids := make([]any, 0, len(a.gridInflight))
	for id := range a.gridInflight {
		grids = append(grids, id)
	}
	tiles := make([]any, 0, len(a.tileInflight))
	for id := range a.tileInflight {
		tiles = append(tiles, id)
	}
	return map[string]any{
		"transition":   a.transition != nil,
		"wsPending":    a.wsPending != nil,
		"dragging":     a.dragging != nil,
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
	return a.transition != nil
}

// thAscentDepth returns the session ascent-stack depth for a pane.
func (a *App) thAscentDepth(paneID string) int {
	pl, ok := a.localIf(paneID)
	if !ok {
		return 0
	}
	return pl.AscentDepth()
}

// thPreviewSigs returns, for the FOCUSED pane's leaf grid, a per-tile
// signature of everything the preview renderer reads: the tile row's content
// identity + framing fields, and — for a well whose child grid is cached —
// the child grid's tile rows too (a well's preview IS its child grid drawn
// small). Read-only over the same cache render reads, so the signature can't
// disagree with pixels by construction. Two captures being equal means "the
// preview is byte-identical"; the I7 spec asserts that across a sibling pane
// while another pane descends/reframes/ascends.
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

// thGridSigs is previewSigs for an EXPLICIT grid id: the same per-tile
// signatures, read straight from the cache with no gesture. Exists because
// observing the cache via clicks is self-defeating — a focus click refetches
// the grid it lands on, healing exactly the divergence a spec wants to see
// (the #156 rejected-optimistic-patch test needs the cache as it IS).
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

// tileSig flattens one tile row's render-relevant fields.
func tileSig(t *rpc.Tile) string {
	return fmt.Sprintf("v%d k%s @%d,%d %dx%d view%d,%d,%g text%d,%d,%d,%d,%s blob%d prev%d url%q alt%q ref%v",
		t.Version, t.Kind, t.X, t.Y, t.W, t.H,
		t.ViewX, t.ViewY, t.ViewZoom,
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
			"anchor":  p.Anchor,
			"path":    stringsToAny(p.Path),
			// The tile this pane is descended into ("" when on a grid) — lets a
			// test tell a shell descent from the url it descended further into.
			"textFocus": p.TextFocus,
			// Viewport center (grid cell coords) + zoom, so a test can drop on a
			// cell guaranteed to be on-screen regardless of the stored framing.
			"cx":   p.Cx,
			"cy":   p.Cy,
			"zoom": p.Zoom,
			// The two ascent-history depths: frames (portal crossings, on the
			// pane) and the session stack (in-namespace descents). Disjoint
			// owners — a spec can assert no flow leaks entries (issue #26).
			"frameDepth":  len(p.Up),
			"ascentDepth": a.thAscentDepth(id),
			// The ids of the tiles this pane RENDERS (its cache contents). The gap
			// between this and the server (the GetGrid oracle) is exactly the
			// create→cache→render / Subscribe-fanout seam where a tile "disappears":
			// present on the server but never drawn.
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

// thLauncher returns the plugin tiles of the NODE GRID (the landing page)
// for the focused pane. Each entry carries the plugin identity plus the
// screen-space center of its tile, to click to enter it. Empty when the
// focused pane isn't at the node grid, or the node grid hasn't loaded yet —
// the driver polls until non-empty, which now also covers grid-fetch latency.
func (a *App) thLauncher(js.Value, []js.Value) any {
	p, r, ok := a.focusedPaneRect()
	if !ok || !a.isNodeGridPane(p) {
		return []any{}
	}
	g, ok := a.c.Grid(a.nodeGrid)
	if !ok {
		return []any{}
	}
	ps := paneToDragdrop(p, r)
	out := make([]any, 0, len(a.plugins))
	for i, pl := range a.plugins {
		t, ok := g.Tiles[a.nodeGrid[:strings.IndexByte(a.nodeGrid, '/')]+"/"+pl.UUID]
		if !ok {
			continue
		}
		// Center of the plugin's tile, in screen pixels.
		sx, sy := ps.CellToScreen(float64(t.X)+float64(t.W)/2, float64(t.Y)+float64(t.H)/2)
		status := pluginStatusName(pl)
		out = append(out, map[string]any{
			"index":         i,
			"kind":          pl.Kind,
			"label":         pl.Label,
			"uuid":          pl.UUID,
			"rootGridID":    pl.RootGridID,
			"scratchGridID": pl.ScratchGridID,
			"infoError":     pl.InfoError,
			"status":        status,
			"x":             sx,
			"y":             sy,
		})
	}
	return out
}

// thPlugins returns the configured plugin list (identity, root/scratch grids,
// pluginhealth classification) with no screen positions — available wherever
// the pane sits, unlike thLauncher which reads the node grid's tiles. Empty
// until ListPlugins lands; the driver polls.
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
		})
	}
	return out
}

// pluginStatusName is the stable string for a plugin's pluginhealth class,
// shared by thLauncher and thPlugins/thPalette.
func pluginStatusName(pl rpc.PluginInfo) string {
	switch pluginhealth.Classify(pl) {
	case pluginhealth.Broken:
		return "broken"
	case pluginhealth.Rootless:
		return "rootless"
	}
	return "enterable"
}

// thPalette returns the creation palette for the focused pane: whether it is
// open, the + button center, and one entry per swatch with its screen rect and
// identity. The rects are the SAME ones paletteTileIndexAt hit-tests, so a click
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
		} else {
			e["kind"] = templateKindName(item.primitive)
		}
		entries = append(entries, e)
	}
	return map[string]any{
		"open":  a.menu.OpenOn(p.ID),
		"plusX": px,
		"plusY": py,
		"items": entries,
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

// templateKindName is the stable string a test uses to pick a primitive swatch.
func templateKindName(k templateKind) string {
	switch k {
	case tplWell:
		return "well"
	case tplMarkdown:
		return "markdown"
	case tplURL:
		return "url"
	case tplShell:
		return "shell"
	case tplPane:
		return "pane"
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

// thBar exposes the bottom bar (issue #212): the band's top edge and every
// segment's rect + identity — workspace crumbs by level, chain crumbs by
// index with the tile/anchor they stand for. Read-only over the exact
// layout drawBottomBar renders and bottomBarClick hit-tests, so a spec's
// click at a segment center is the click the user would make.
func (a *App) thBar(js.Value, []js.Value) any {
	bx, top, bw, ok := a.bottomBarRect()
	if !ok {
		return map[string]any{"top": 0.0, "height": wsbar.RowH, "segments": []any{}}
	}
	chain := a.navChain()
	segs := a.bottomBarSegments(chain)
	out := make([]any, 0, len(segs))
	for _, s := range segs {
		// Segment X is emitted ABSOLUTE (the band lives inside the focused
		// pane since #220), so specs click hook coordinates verbatim. Index
		// addresses the FULL chain (left-truncation drops leading crumbs).
		e := map[string]any{
			"x": bx + s.X, "w": s.W, "index": s.Index,
		}
		nc := chain[s.Index]
		if nc.paneTile {
			e["kind"] = "pane"
			e["level"] = nc.wsLevel
			e["tileID"] = nc.tileID
		} else {
			e["kind"] = "chain"
			e["level"] = nc.treeLevel
			e["anchor"] = nc.crumb.Anchor
			e["tileID"] = nc.crumb.TileID
			e["text"] = nc.crumb.Text
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
	// The centered current-pane title (2026-07-30 tweak): the exact rect
	// drawBarTitle renders and bottomBarClick hit-tests.
	if x, w, label, editable, muted, ok := a.barTitleGeom(); ok {
		res["title"] = map[string]any{
			"x": x, "w": w, "label": label, "editable": editable, "muted": muted,
		}
	}
	return res
}

// thRawRows returns how many visual rows the CANVAS painter would produce
// for the focused text descent's current textarea content — the wrap-parity
// oracle (issue #216). A spec compares this against the textarea's own
// scrollHeight-derived row count, crossing the browser-soft-wrap vs
// canvas-wrap seam with the SAME bytes on both sides.
func (a *App) thRawRows(js.Value, []js.Value) any {
	p := a.tree.FocusedPane()
	if p == nil || p.TextFocus == "" || !a.textTextarea.Truthy() {
		return -1
	}
	src := a.textTextarea.Get("value").String()
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
