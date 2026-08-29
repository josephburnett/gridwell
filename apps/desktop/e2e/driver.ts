import type { Page } from '@playwright/test';
import { getGrid, getTileContent, GridSnapshot } from './oracle';

// GridwellDriver is the reusable gesture layer the e2e tests are written
// against. It composes two sources of truth:
//   - window.__gridwellTest — the renderer's read-only introspection hook
//     (installed only under ?e2e=1). Supplies WHERE to click (pane rects,
//     palette swatch rects, the + button, cell→screen centers) and an idle()
//     settle signal so we wait on state, never on sleeps.
//   - the server oracle (getGrid) — supplies the GROUND TRUTH of what a gesture
//     actually created.
// All clicks go through window.mouse, which dispatches genuine CDP input the
// canvas listeners receive exactly like a real user's mouse.

export interface PaneInfo {
  id: string;
  x: number;
  y: number;
  w: number;
  h: number;
  focused: boolean;
  gridID: string;
  anchor: string;
  path: string[];
  textFocus: string;
  // The pane's grid is a cache-served memory (the wire stale bit, #256).
  stale?: boolean;
  textMode: string;
  cx: number;
  cy: number;
  zoom: number;
  // Depth of the portal Up stack / the saved-ascent stack — the wasm hook
  // emits both and specs assert on them.
  frameDepth: number;
  ascentDepth: number;
  // The ids of the tiles this pane renders (its cache contents). A tile present
  // on the server (the getGrid oracle) but missing here is the "disappeared" bug.
  tileIds: string[];
}

export interface PaletteItem {
  index: number;
  // Plugin swatches (top row): click descends into the plugin, drag drops an
  // exit-well link. isPlugin distinguishes them from the primitive swatches;
  // for a plugin, kind is the plugin kind (home, fs, proc, gitlab, …), label/uuid
  // identify it, and rootGridID/status mirror the pluginhealth surface.
  isPlugin: boolean;
  kind: string;
  label?: string;
  uuid?: string;
  rootGridID?: string;
  status?: string;
  // A plugin-declared menu entry (#258): the entry id (e.g. fs "search").
  // Creation entries are !isPlugin rows after the primitives; root entries
  // ride a plugin-shaped row.
  entry?: string;
  x: number;
  y: number;
  w: number;
  h: number;
}

// PluginDescriptor is one configured plugin as the client knows it — the
// position-free plugin list (window.__gridwellTest.plugins()), available
// wherever the focused pane sits.
export interface PluginDescriptor {
  index: number;
  kind: string;
  label: string;
  uuid: string;
  rootGridID: string;
  scratchGridID: string;
  infoError: string;
  status: string;
  // The plugin's persisted root view from the handshake (zero zoom =
  // never set) — the server-truth oracle for a root-grid reframe.
  rootViewCx: number;
  rootViewCy: number;
  rootViewZoom: number;
}

export interface PaletteInfo {
  open: boolean;
  plusX: number;
  plusY: number;
  items: PaletteItem[];
}

export class GridwellDriver {
  constructor(
    private win: Page,
    public readonly origin: string,
  ) {}

  // ── Introspection (read-only hook) ──────────────────────────────────────

  panes(): Promise<PaneInfo[]> {
    return this.win.evaluate(() => (window as any).__gridwellTest.panes());
  }

  async focused(): Promise<PaneInfo> {
    const ps = await this.panes();
    const f = ps.find((p) => p.focused);
    if (!f) throw new Error('no focused pane');
    return f;
  }

  // plugins returns the configured plugin list with health classification.
  // Waits for Handshake to land.
  async plugins(): Promise<PluginDescriptor[]> {
    await this.win.waitForFunction(() => (window as any).__gridwellTest.plugins().length > 0, null, {
      timeout: 15_000,
    });
    return this.win.evaluate(() => (window as any).__gridwellTest.plugins());
  }

  // localPaneIds returns the pane ids that currently hold per-pane client state
  // (a.locals). After a pane is collapsed its id must disappear here — the proof
  // that forgetPane tore the per-pane state down rather than orphaning it.
  localPaneIds(): Promise<string[]> {
    return this.win.evaluate(() => (window as any).__gridwellTest.localPaneIds());
  }

  // collapseLeftPane LEFT-drags the divider of a two-pane split hard to the
  // left edge, crushing the left pane below the close threshold so the
  // release collapses it (#203).
  async collapseLeftPane(): Promise<void> {
    const [left] = (await this.panes()).slice().sort((a, b) => a.x - b.x);
    const y = left.y + left.h / 2;
    // #203: crush-to-close is the LEFT button's release semantics now.
    await this.leftDragScreen(left.x + left.w - 2, y, left.x + 6, y);
  }

  palette(): Promise<PaletteInfo> {
    return this.win.evaluate(() => (window as any).__gridwellTest.palette());
  }

  // barName returns the bottom bar's centered current-pane TITLE (issue
  // #213, 2026-07-30 tweak): the focused pane's name, with the bar band's
  // geometry.
  // bar returns the raw bar hook: the band geometry plus every segment's
  // rect and identity (chain crumbs carry anchor/tileID and, for root
  // crumbs, the glyph the crumb renders — the #264 pin). Every pane wears
  // a band since #267 — pass a paneID for an unfocused pane's bar; no
  // arg reads the focused pane's.
  async bar(paneID?: string): Promise<{
    top: number;
    left: number;
    width: number;
    height: number;
    segments: Array<{
      kind: string;
      x: number;
      w: number;
      index: number;
      level: number;
      anchor?: string;
      tileID?: string;
      glyph?: string;
    }>;
  }> {
    return this.win.evaluate((id) => (window as any).__gridwellTest.bar(id), paneID ?? '');
  }

  async barName(): Promise<{
    x: number;
    w: number;
    top: number;
    height: number;
    label: string;
    editable: boolean;
    muted: boolean;
  }> {
    const bar = await this.win.evaluate(() => (window as any).__gridwellTest.bar());
    if (!bar.title) {
      throw new Error('bar has no current-pane title (boot-blank pane?)');
    }
    const t = bar.title;
    return { x: t.x, w: t.w, top: bar.top, height: bar.height, label: t.label, editable: t.editable, muted: t.muted };
  }

  // clickBarName clicks the centered title: LEFT toggles the tmux-style
  // pane zoom, RIGHT opens the rename input when editable — the old
  // name-bubble contract, now living in the bar (2026-07-30 buttons).
  async clickBarName(button: 'left' | 'right' = 'left'): Promise<void> {
    const b = await this.barName();
    await this.win.mouse.click(b.x + b.w / 2, b.top + b.height / 2, { button });
  }

  // leaveWorkspace exits down to workspace stack level toLevel (0 = the
  // session) via the one-chain nav (#245): a crumb click GOES THERE, so
  // leaving means clicking the last crumb BEFORE the pane-tile boundary of
  // level toLevel+1 — the outer chain's tail, which lands exactly where the
  // old leave-crumb click did. waitIdle covers the return animation (a
  // click during it is deliberately swallowed).
  async leaveWorkspace(toLevel = 0): Promise<void> {
    const bar = await this.win.evaluate(() => (window as any).__gridwellTest.bar());
    const boundary = bar.segments.findIndex(
      (s: any) => s.kind === 'pane' && s.level === toLevel + 1,
    );
    if (boundary <= 0) throw new Error(`no crumb before the level-${toLevel + 1} boundary`);
    const seg = bar.segments[boundary - 1];
    await this.win.mouse.click(seg.x + seg.w / 2, bar.top + bar.height / 2);
    await this.waitIdle();
  }

  // cellCenter maps a grid cell to screen coordinates — and REFUSES a point
  // outside the pane's rect. An off-pane (or off-viewport) point is always a
  // spec bug (the cell isn't where the viewport shows at this zoom), and CDP
  // silently drops events dispatched outside the window: the gesture half
  // fires, dragging never clears, and waitIdle hangs with no clue (the #195 /
  // #203 off-viewport class). Fail loudly with the numbers instead.
  async cellCenter(paneID: string, cx: number, cy: number): Promise<{ x: number; y: number }> {
    const pt = await this.win.evaluate(
      ([id, x, y]) => (window as any).__gridwellTest.cellCenter(id, x, y),
      [paneID, cx, cy] as [string, number, number],
    );
    const panes = await this.panes();
    const p = panes.find((pn) => pn.id === paneID);
    if (p && (pt.x < p.x || pt.x >= p.x + p.w || pt.y < p.y || pt.y >= p.y + p.h)) {
      throw new Error(
        `cellCenter(${cx},${cy}) = (${pt.x},${pt.y}) lies outside pane ${paneID} ` +
          `(${p.x},${p.y} ${p.w}x${p.h}) — pick a cell inside the current viewport`,
      );
    }
    return pt;
  }

  // waitIdle blocks until the renderer reports no transition, drag, or in-flight
  // fetch. window.mouse dispatches events synchronously into the wasm handlers,
  // so by the time a gesture's mouse.up() resolves the handler has already armed
  // any transition/fetch — this then waits for it to settle.
  async waitIdle(timeout = 20_000): Promise<void> {
    await this.win.waitForFunction(() => (window as any).__gridwellTest.idle(), null, { timeout });
  }

  // ── Gestures ────────────────────────────────────────────────────────────

  // enterPlugin puts the focused pane at the given plugin's root grid
  // (matched by kind or label). Boot already lands on the FIRST configured
  // plugin's root, so this is often a no-op; otherwise the + menu's plugin
  // swatch descends (a portal).
  async enterPlugin(match: string): Promise<void> {
    const pls = await this.plugins();
    const pl = pls.find((p) => p.kind === match || p.label === match);
    if (!pl) throw new Error(`no plugin matching ${match}; have ${pls.map((p) => p.kind)}`);
    const f = await this.focused();
    if (pl.rootGridID && f.gridID === pl.rootGridID) return; // already there
    await this.clickPluginSwatch(match);
  }

  // openPalette opens the focused pane's creation menu by clicking its + button
  // (the pane is already focused after a descent, so a single click opens it).
  // No-op when already open — the + click is a toggle, and a double open must
  // not close it.
  async openPalette(): Promise<void> {
    const pal = await this.palette();
    if (pal.open) return;
    await this.win.mouse.click(pal.plusX, pal.plusY);
    await this.win.waitForFunction(() => (window as any).__gridwellTest.palette().open, null, {
      timeout: 5_000,
    });
  }

  // focusPane left-clicks the empty center of the given pane to move focus to
  // it. The grid is empty after entry/split, so the press lands on no tile — a
  // pure focus click (no descend, no pan). Used to exercise the focus-change
  // rules (e.g. the + menu closing when focus leaves its pane).
  async focusPane(p: PaneInfo): Promise<void> {
    await this.win.mouse.click(p.x + p.w / 2, p.y + p.h / 2);
    await this.waitIdle();
  }

  // dragCreate drags the palette swatch of the given primitive kind ("well",
  // "markdown", "url", "shell") onto cell (cx, cy) of the focused pane: press on
  // the swatch, move past the 4px drag threshold, drag to the cell, release.
  async dragCreate(kind: string, cx: number, cy: number): Promise<void> {
    const pal = await this.palette();
    const item = pal.items.find((i) => !i.isPlugin && i.kind === kind);
    if (!item) throw new Error(`no palette primitive ${kind}; have ${pal.items.map((i) => i.kind)}`);
    await this.dragSwatchToCell(item, cx, cy);
  }

  // dragPluginLink drags a plugin swatch (matched by kind or label) onto cell
  // (cx, cy) of the focused pane — the drop-a-link gesture, distinct from
  // clickPluginSwatch's descend.
  async dragPluginLink(match: string, cx: number, cy: number): Promise<void> {
    const pal = await this.palette();
    const item = pal.items.find((i) => i.isPlugin && (i.kind === match || i.label === match));
    if (!item) throw new Error(`no plugin swatch ${match}; have ${pal.items.map((i) => i.kind)}`);
    await this.dragSwatchToCell(item, cx, cy);
  }

  private async dragSwatchToCell(item: PaletteItem, cx: number, cy: number): Promise<void> {
    const sx = item.x + item.w / 2;
    const sy = item.y + item.h / 2;
    const f = await this.focused();
    const target = await this.cellCenter(f.id, cx, cy);

    const m = this.win.mouse;
    await m.move(sx, sy);
    await m.down();
    await m.move(sx + 10, sy + 10); // exceed the 4px threshold → arm the drag
    await m.move(target.x, target.y, { steps: 8 });
    await m.up();
    await this.waitIdle();
  }

  // clickPaletteSwatch opens the palette and single-clicks (no drag) the
  // primitive swatch of the given kind. A click is distinct from dragCreate's
  // drag: for the url swatch it opens the ephemeral-visit modal rather than
  // placing a tile.
  async clickPaletteSwatch(kind: string): Promise<void> {
    await this.openPalette();
    const pal = await this.palette();
    const item = pal.items.find((i) => !i.isPlugin && i.kind === kind);
    if (!item) throw new Error(`no palette primitive ${kind}; have ${pal.items.map((i) => i.kind)}`);
    await this.win.mouse.click(item.x + item.w / 2, item.y + item.h / 2);
  }

  // clickPluginSwatch opens the palette and single-clicks (no drag) the plugin
  // swatch matched by kind or label — the "descend from the menu" gesture; the
  // pane portals into the plugin's root grid.
  async clickPluginSwatch(match: string): Promise<void> {
    await this.openPalette();
    const pal = await this.palette();
    const item = pal.items.find((i) => i.isPlugin && (i.kind === match || i.label === match));
    if (!item) throw new Error(`no plugin swatch ${match}; have ${pal.items.map((i) => i.kind)}`);
    await this.win.mouse.click(item.x + item.w / 2, item.y + item.h / 2);
    await this.waitIdle();
  }

  // shellVisitURL fires the focused live shell's url-click path (the exact
  // callback xterm's link provider runs when a url in the terminal is clicked).
  // A terminal-cell link can't be hit-tested from the canvas, so the e2e drives
  // it through this e2e-only hook.
  async shellVisitURL(url: string): Promise<void> {
    await this.win.evaluate((u) => (window as any).__gridwellTest.shellVisitURL(u), url);
    await this.waitIdle();
  }

  // descendCell single-clicks the center of cell (cx, cy) in the focused pane to
  // descend into the tile there, then waits for the zoom animation to settle.
  async descendCell(cx: number, cy: number): Promise<void> {
    const f = await this.focused();
    const c = await this.cellCenter(f.id, cx, cy);
    await this.win.mouse.click(c.x, c.y);
    await this.waitIdle();
  }

  // ── Tile gestures (left/right button drags over the canvas) ───────────────

  // NUDGE is the first move of every synthetic drag: comfortably past the
  // 4px canvas/preload drag threshold so the press reads as a drag.
  private static readonly NUDGE = 8;

  // dragTileCell left-drags the tile at cell (fromCx,fromCy) to (toCx,toCy) in
  // the focused pane — the move gesture. Server-observable: the tile's X/Y change.
  async dragTileCell(fromCx: number, fromCy: number, toCx: number, toCy: number): Promise<void> {
    await this.dragCell(fromCx, fromCy, toCx, toCy, 'left');
  }

  // cloneTileCell right-drags from the CENTER of the tile at (fromCx,fromCy) to
  // (toCx,toCy) — the clone gesture (right-drag from a tile's inner third). A new
  // independent tile lands at the destination cell.
  async cloneTileCell(fromCx: number, fromCy: number, toCx: number, toCy: number): Promise<void> {
    await this.dragCell(fromCx, fromCy, toCx, toCy, 'right');
  }

  // dragCell is the shared press→nudge→drag→release over two cell centers.
  private async dragCell(fromCx: number, fromCy: number, toCx: number, toCy: number, button: 'left' | 'right'): Promise<void> {
    const f = await this.focused();
    const from = await this.cellCenter(f.id, fromCx, fromCy);
    const to = await this.cellCenter(f.id, toCx, toCy);
    const m = this.win.mouse;
    await m.move(from.x, from.y);
    await m.down({ button });
    await m.move(from.x + GridwellDriver.NUDGE, from.y + GridwellDriver.NUDGE);
    await m.move(to.x, to.y, { steps: 8 });
    await m.up({ button });
    await this.waitIdle();
  }

  // cloneDragAcrossPanes right-drags (the CLONE gesture — a copy everywhere)
  // the tile at cell (fcx,fcy) of pane fromID onto cell (tcx,tcy) of pane
  // toID. Across a plugin boundary a leaf copies its bytes; a SOLID well is
  // refused (deep copy unimplemented); a link tile copies as another link.
  async cloneDragAcrossPanes(fromID: string, fcx: number, fcy: number, toID: string, tcx: number, tcy: number): Promise<void> {
    await this.dragAcrossPanes(fromID, fcx, fcy, toID, tcx, tcy, 'right');
  }

  // leftDragAcrossPanes left-drags the tile at cell (fcx,fcy) of pane fromID
  // onto cell (tcx,tcy) of pane toID. Within one plugin this MOVES; across a
  // plugin boundary it creates a LINK in the destination and the source stays
  // put (owner decision 2026-07-19 — there is no cross-plugin move).
  async leftDragAcrossPanes(fromID: string, fcx: number, fcy: number, toID: string, tcx: number, tcy: number): Promise<void> {
    await this.dragAcrossPanes(fromID, fcx, fcy, toID, tcx, tcy, 'left');
  }

  private async dragAcrossPanes(fromID: string, fcx: number, fcy: number, toID: string, tcx: number, tcy: number, button: 'left' | 'right'): Promise<void> {
    const from = await this.cellCenter(fromID, fcx, fcy);
    const to = await this.cellCenter(toID, tcx, tcy);
    const m = this.win.mouse;
    await m.move(from.x, from.y);
    await m.down({ button });
    await m.move(from.x + GridwellDriver.NUDGE, from.y + GridwellDriver.NUDGE);
    await m.move(to.x, to.y, { steps: 10 });
    await m.up({ button });
    await this.waitIdle();
  }

  // resizeTileCell right-drags from NEAR THE CORNER of the 1x1 tile at (cx,cy) —
  // outside its center third, so the gesture is tile-resize not clone — out to
  // the center of (toCx,toCy). The tile's footprint (W/H) rubber-bands to the
  // bounding box of (pin corner, cursor).
  async resizeTileCell(cx: number, cy: number, toCx: number, toCy: number): Promise<void> {
    const f = await this.focused();
    // A point ~0.35 cells past center toward the bottom-right corner: outside
    // the inner-third center zone (±~0.17), so right-down arms resize.
    const center = await this.cellCenter(f.id, cx, cy);
    const next = await this.cellCenter(f.id, cx + 1, cy + 1);
    const corner = { x: center.x + 0.35 * (next.x - center.x), y: center.y + 0.35 * (next.y - center.y) };
    const to = await this.cellCenter(f.id, toCx, toCy);
    const m = this.win.mouse;
    await m.move(corner.x, corner.y);
    await m.down({ button: 'right' });
    await m.move(corner.x + GridwellDriver.NUDGE, corner.y + GridwellDriver.NUDGE);
    await m.move(to.x, to.y, { steps: 8 });
    await m.up({ button: 'right' });
    await this.waitIdle();
  }

  // deleteTileCell left-drags the tile at (cx,cy) onto the focused pane's +
  // button (which becomes a trashcan during a drag) — the delete gesture. The
  // tile is removed server-side.
  async deleteTileCell(cx: number, cy: number): Promise<void> {
    const f = await this.focused();
    const from = await this.cellCenter(f.id, cx, cy);
    const pal = await this.palette();
    const m = this.win.mouse;
    await m.move(from.x, from.y);
    await m.down({ button: 'left' });
    await m.move(from.x + GridwellDriver.NUDGE, from.y + GridwellDriver.NUDGE);
    await m.move(pal.plusX, pal.plusY, { steps: 8 });
    await m.up({ button: 'left' });
    await this.waitIdle();
  }

  // ── Pane gestures (right-drag in screen space) ────────────────────────────

  // rightDragScreen presses the right button at (fromX,fromY), nudges past the
  // threshold, drags to (toX,toY) and releases — the raw pane-gesture driver used
  // for split / swap / divider-resize.
  async rightDragScreen(fromX: number, fromY: number, toX: number, toY: number): Promise<void> {
    const m = this.win.mouse;
    await m.move(fromX, fromY);
    await m.down({ button: 'right' });
    await m.move(fromX - GridwellDriver.NUDGE, fromY);
    await m.move(toX, toY, { steps: 10 });
    await m.up({ button: 'right' });
    await this.waitIdle();
  }

  // splitFocusedPaneVertical right-drags inward from the focused pane's right
  // edge band, splitting it into two side-by-side panes.
  async splitFocusedPaneVertical(): Promise<void> {
    const p = await this.focused();
    const y = p.y + p.h / 2;
    // Start in the right-edge resize band (~5px in), drag to mid-pane.
    await this.rightDragScreen(p.x + p.w - 5, y, p.x + p.w * 0.45, y);
  }

  // splitFocusedPaneHorizontal right-drags inward from the focused pane's
  // bottom edge band, splitting it into two stacked panes.
  async splitFocusedPaneHorizontal(): Promise<void> {
    const p = await this.focused();
    // 30% across: the bottom edge band is under the bar (issue #220), and a
    // right-down there falls through to the split gesture EXCEPT on the
    // centered title (rename) — so grab between the crumbs and the title.
    const x = p.x + p.w * 0.3;
    await this.rightDragScreen(x, p.y + p.h - 5, x, p.y + p.h * 0.45);
  }

  // hDividerGeom returns the midpoint of the horizontal boundary between two
  // STACKED panes (the top pane's bottom edge).
  private async hDividerGeom(): Promise<{ x: number; y: number; topPaneH: number; topId: string }> {
    const ps = (await this.panes()).slice().sort((a, b) => a.y - b.y);
    if (ps.length < 2) throw new Error('hDividerGeom needs two panes');
    const top = ps[0];
    return { x: top.x + top.w / 2, y: top.y + top.h, topPaneH: top.h, topId: top.id };
  }

  // resizeHDivider drags the stacked-pane boundary by dy (down grows the top
  // pane) with the given button; returns the top pane's height before/after.
  async resizeHDivider(
    button: 'left' | 'right',
    dy: number,
  ): Promise<{ before: number; after: number }> {
    const g = await this.hDividerGeom();
    // Grab from BELOW the boundary (the lower pane's top band): the upper
    // side can be the focused pane's bar band (issue #220), which owns
    // left-clicks. #217 made the two sides of a border equivalent.
    if (button === 'right') {
      await this.rightDragScreen(g.x, g.y + 2, g.x, g.y + dy);
    } else {
      await this.leftDragScreen(g.x, g.y + 2, g.x, g.y + dy);
    }
    const after = (await this.panes()).find((p) => p.id === g.topId);
    return { before: g.topPaneH, after: after ? after.h : 0 };
  }

  // leftDragScreen presses the LEFT button at (fromX,fromY), nudges past the
  // threshold, drags to (toX,toY) and releases — used for the clamped left-drag
  // pane-boundary resize.
  async leftDragScreen(fromX: number, fromY: number, toX: number, toY: number): Promise<void> {
    const m = this.win.mouse;
    await m.move(fromX, fromY);
    await m.down({ button: 'left' });
    await m.move(fromX - GridwellDriver.NUDGE, fromY);
    await m.move(toX, toY, { steps: 10 });
    await m.up({ button: 'left' });
    await this.waitIdle();
  }

  // dividerX returns the x of the vertical boundary between the two side-by-side
  // panes (the left pane's right edge) and the shared mid-height y.
  private async dividerGeom(): Promise<{ x: number; y: number; leftPaneW: number; leftId: string }> {
    const ps = (await this.panes()).slice().sort((a, b) => a.x - b.x);
    if (ps.length < 2) throw new Error('dividerGeom needs two panes');
    const left = ps[0];
    return { x: left.x + left.w, y: left.y + left.h / 2, leftPaneW: left.w, leftId: left.id };
  }

  // resizeDividerRight right-drags the pane divider left by dx, collapsing/
  // shrinking the left pane. Returns the left pane's width before and after.
  async resizeDivider(button: 'left' | 'right', dx: number): Promise<{ before: number; after: number }> {
    const g = await this.dividerGeom();
    if (button === 'right') {
      await this.rightDragScreen(g.x - 2, g.y, g.x + dx, g.y);
    } else {
      await this.leftDragScreen(g.x - 2, g.y, g.x + dx, g.y);
    }
    const after = (await this.panes()).find((p) => p.id === g.leftId);
    return { before: g.leftPaneW, after: after ? after.w : 0 };
  }

  // ascendViaCrumb left-clicks the previous chain crumb in the focused
  // pane's bottom bar — THE bar ascent gesture since #222 (the old
  // right-click-the-circle ascend is gone).
  async ascendViaCrumb(): Promise<void> {
    const bar = await this.win.evaluate(() => (window as any).__gridwellTest.bar());
    const depth = await this.win.evaluate(() => (window as any).__gridwellTest.workspace().depth);
    // Only the CURRENT tree's crumbs (#245: the one chain also carries the
    // outer context — clicking those would cross the workspace boundary).
    const chain = (bar.segments as any[]).filter((s) => s.kind === 'chain' && s.level === depth);
    if (chain.length < 2) return; // nothing to ascend to — a graceful no-op
    const seg = chain[chain.length - 2];
    await this.win.mouse.click(seg.x + seg.w / 2, bar.top + bar.height / 2);
    // The ascent animates and input is blocked until it settles; callers
    // historically follow this helper with an immediate next gesture, so
    // settle here rather than in 27 call sites.
    await this.waitIdle();
  }

  // ── View gestures ─────────────────────────────────────────────────────────

  // middleClickPane middle-clicks the CENTER of the focused pane's rect —
  // the universal ascend, which is position-independent. Prefer this over
  // middleClickCell for bare ascents: a computed cell center can land
  // OUTSIDE the pane at high zoom (one cell below center is off-pane once
  // cells exceed the half-pane — the stack-hygiene silent no-op, #195),
  // and an off-pane click is swallowed without a trace.
  async middleClickPane(): Promise<void> {
    const f = await this.focused();
    await this.win.mouse.click(f.x + f.w / 2, f.y + f.h / 2, { button: 'middle' });
    await this.waitIdle();
  }

  // middleClickCell middle-clicks the center of a cell — the universal ascend
  // shortcut when done over a descended pane.
  async middleClickCell(cx: number, cy: number): Promise<void> {
    const f = await this.focused();
    const c = await this.cellCenter(f.id, cx, cy);
    await this.win.mouse.click(c.x, c.y, { button: 'middle' });
    await this.waitIdle();
  }

  // wheelAtFocusedCenter scrolls the wheel over the focused pane's center to zoom
  // the grid. Negative dy zooms in.
  async wheelAtFocusedCenter(dy: number): Promise<void> {
    const p = await this.focused();
    await this.win.mouse.move(p.x + p.w / 2, p.y + p.h / 2);
    await this.win.mouse.wheel(0, dy);
    await this.waitIdle();
  }

  // panFocusedGrid left-drags from one EMPTY cell to another to pan the grid
  // (no tile under the press point), shifting the viewport center.
  async panFocusedGrid(fromCx: number, fromCy: number, toCx: number, toCy: number): Promise<void> {
    await this.dragCell(fromCx, fromCy, toCx, toCy, 'left');
  }

  // ── Oracle ──────────────────────────────────────────────────────────────

  getGrid(gridID: string): Promise<GridSnapshot> {
    return getGrid(this.origin, gridID);
  }

  getTileContent(tileID: string): Promise<string> {
    return getTileContent(this.origin, tileID);
  }

  // typeText sends literal keystrokes to whatever has keyboard focus (a text
  // descent's editor), then waits for the debounced save to settle.
  async typeText(s: string): Promise<void> {
    await this.win.keyboard.type(s);
    await this.waitIdle();
  }


  // clickScreen single-left-clicks a raw screen coordinate.
  async clickScreen(x: number, y: number): Promise<void> {
    await this.win.mouse.click(x, y);
    await this.waitIdle();
  }

  // toggleTextMode left-clicks the bar-slot toggle of a file descent — the
  // DOM button that flips the focused pane between raw text and rendered
  // markdown (the same slot the + button occupies on a grid pane).
  async toggleTextMode(): Promise<void> {
    const pal = await this.palette();
    await this.win.mouse.click(pal.plusX, pal.plusY);
    await this.waitIdle();
  }


  // textareaInfo returns the current textarea overlay binding: the pane it
  // covers, the tile it's bound to, and whether it has content (textareaReady).
  // Returns null when no pane is in raw-text mode. Used to assert the overlay
  // covers exactly one pane (the focused descended one), not preview nodes in
  // other panes — the issue #35 mechanism B invariant.
  textareaInfo(): Promise<{ paneID: string; tileID: string; hasContent: boolean; x: number; y: number; w: number; h: number } | null> {
    return this.win.evaluate(() => (window as any).__gridwellTest.textareaInfo());
  }

  // textareaValue reads the raw-text overlay's current buffer straight from
  // the DOM — what the user actually sees in a text descent. null when no
  // textarea overlay exists.
  textareaValue(): Promise<string | null> {
    return this.win.evaluate(() => {
      const ta = document.querySelector<HTMLTextAreaElement>('#gw-text-editor');
      return ta ? ta.value : null;
    });
  }
}
