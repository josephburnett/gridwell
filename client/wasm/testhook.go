//go:build js && wasm

package main

import (
	"fmt"
	"sort"
	"strings"
	"syscall/js"

	"github.com/josephburnett/gridwell/client/anim"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/pluginhealth"
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
		"origin":        js.FuncOf(a.thOrigin),
		"panes":         js.FuncOf(a.thPanes),
		"previewSigs":   js.FuncOf(a.thPreviewSigs),
		"transitioning": js.FuncOf(a.thTransitioning),
		"embedHits":     js.FuncOf(a.thEmbedHits),
		"setTransitionMs": js.FuncOf(func(_ js.Value, args []js.Value) any {
			// e2e-only ACTION (like shellVisitURL): stretch the transition
			// clock so a spec can deterministically land an event mid-flight.
			if len(args) == 1 {
				totalTransitionMs = args[0].Float()
			}
			return nil
		}),
		"launcher":      js.FuncOf(a.thLauncher),
		"palette":       js.FuncOf(a.thPalette),
		"cellCenter":    js.FuncOf(a.thCellCenter),
		"shellVisitURL": js.FuncOf(a.thShellVisitURL),
		"localPaneIds":  js.FuncOf(a.thLocalPaneIds),
		"renderedCaret": js.FuncOf(a.thRenderedCaret),
		"textInnerBox":  js.FuncOf(a.thTextInnerBox),
		"textareaInfo":  js.FuncOf(a.thTextareaInfo),
		"errors":        js.FuncOf(a.thErrors),
		"traces":        js.FuncOf(a.thTraces),
		"shellRenderer": js.FuncOf(a.thShellRenderer),
	}))
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

// thRenderedCaret returns the focused pane's rendered-mode caret as
// {offset, has}: the source byte offset typing will splice at, or has=false
// when no caret is placed. Pure read of the per-pane state; lets an e2e
// assert click-to-place landed where the click said.
func (a *App) thRenderedCaret(js.Value, []js.Value) any {
	p := a.tree.FocusedPane()
	if p == nil {
		return map[string]any{"has": false, "offset": 0}
	}
	pl, ok := a.localIf(p.ID)
	if !ok {
		return map[string]any{"has": false, "offset": 0}
	}
	off, has := pl.Caret()
	return map[string]any{"has": has, "offset": off}
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
// pixels — the same textInnerBox the painter and caret hit-tests use, so an
// e2e can click a known position inside the rendered text. Empty when the
// focused pane is not descended into a file.
func (a *App) thTextInnerBox(js.Value, []js.Value) any {
	p, r, ok := a.focusedPaneRect()
	if !ok || p.TextFocus == "" {
		return nil
	}
	x, y, w, h := textInnerBox(p, r)
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
		a.dragging == nil &&
		len(a.gridInflight) == 0 &&
		len(a.tileInflight) == 0
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

// thEmbedHits exposes the embeds drawn in the last frame — the same hit
// rects the click handler resolves against, so a spec can find WHERE an
// embed rendered and whether its target resolved. Read-only render scratch.
func (a *App) thEmbedHits(js.Value, []js.Value) any {
	out := make([]any, 0, len(a.embedHits))
	for _, h := range a.embedHits {
		out = append(out, map[string]any{
			"paneId": h.paneID,
			"href":   h.href,
			"tileId": h.tileID,
			"x":      h.x, "y": h.y, "w": h.w, "h": h.h,
		})
	}
	return out
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
		status := "enterable"
		switch pluginhealth.Classify(pl) {
		case pluginhealth.Broken:
			status = "broken"
		case pluginhealth.Rootless:
			status = "rootless"
		}
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

// thPalette returns the creation palette for the focused pane: whether it is
// open, the + button center, and one entry per swatch with its screen rect and
// identity. The rects are the SAME ones paletteTileIndexAt hit-tests, so a click
// at an entry's center lands on that swatch.
func (a *App) thPalette(js.Value, []js.Value) any {
	p, r, ok := a.focusedPaneRect()
	if !ok {
		return map[string]any{"open": false}
	}
	px, py := plusButtonCenter(p, r)
	items := a.paletteItems(p)
	entries := make([]any, 0, len(items))
	for i, item := range items {
		tx, ty, tw, th := a.paletteTileRect(p, r, i)
		e := map[string]any{
			"index": i,
			"kind":  templateKindName(item.primitive),
			"x":     tx,
			"y":     ty,
			"w":     tw,
			"h":     th,
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
