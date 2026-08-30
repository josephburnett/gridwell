import type { Page } from '@playwright/test';
import { getGrid, getTileContent, GridSnapshot } from './oracle';

// GridwellDriver is the reusable gesture layer the e2e tests are written
// against. It composes two sources of truth:
//   - window.__gridwellTest, the renderer's read-only introspection hook,
//     installed only under ?e2e=1. It supplies where to click (pane rects,
//     palette swatch rects, the + button, cell-to-screen centers) and an idle()
//     settle signal, so a spec waits on state rather than on sleeps.
//   - the server oracle (getGrid), which supplies what a gesture actually
//     created.
// Every click goes through window.mouse, which dispatches real CDP input the
// canvas listeners receive exactly as they would a user's mouse.

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
  // The pane's grid is a cache-served memory: the wire's stale bit.
  stale?: boolean;
  textMode: string;
  cx: number;
  cy: number;
  zoom: number;
  // How many doorways deep the pane's place stack is; 0 is its root grid.
  // There is one stack, so a leaked frame has nowhere to hide.
  placeDepth: number;
  // The ids of the tiles this pane renders, i.e. its cache contents. A tile the
  // getGrid oracle sees but this does not is the "disappeared" bug.
  tileIds: string[];
}

export interface PaletteItem {
  index: number;
  // Plugin swatches sit in the top row: a click descends into the plugin, a
  // drag drops an exit-well link. isPlugin tells them from the primitive
  // swatches. For a plugin, kind is the plugin kind (fs, proc, gitlab, …),
  // label and uuid identify it, and rootGridID and status mirror the
  // pluginhealth surface.
  isPlugin: boolean;
  kind: string;
  label?: string;
  uuid?: string;
  rootGridID?: string;
  status?: string;
  // A plugin-declared menu entry's id, such as fs "search". Creation entries
  // are !isPlugin rows after the primitives; root entries ride a plugin-shaped
  // row.
  entry?: string;
  x: number;
  y: number;
  w: number;
  h: number;
}

// PluginDescriptor is one configured plugin as the client knows it: the
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
  // The plugin's persisted root view from the handshake; zoom 0 means never
  // set. This is the server-side oracle for a root-grid reframe.
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
  // (a.locals). After a pane collapses its id must disappear here, which is the
  // proof that forgetPane tore the per-pane state down rather than orphaning it.
  localPaneIds(): Promise<string[]> {
    return this.win.evaluate(() => (window as any).__gridwellTest.localPaneIds());
  }

  // collapseLeftPane left-drags the divider of a two-pane split hard to the left
  // edge, crushing the left pane below the close threshold so the release
  // collapses it.
  async collapseLeftPane(): Promise<void> {
    const [left] = (await this.panes()).slice().sort((a, b) => a.x - b.x);
    const y = left.y + left.h / 2;
    // Crush-to-close is the left button's release semantics.
    await this.leftDragScreen(left.x + left.w - 2, y, left.x + 6, y);
  }

  palette(): Promise<PaletteInfo> {
    return this.win.evaluate(() => (window as any).__gridwellTest.palette());
  }

  // bar returns the raw bar hook: the band geometry plus every segment's rect
  // and identity. Chain crumbs carry anchor and tileID, and a root crumb also
  // carries the glyph it renders. Every pane wears a band, so pass a paneID to
  // read an unfocused pane's bar; with no argument it reads the focused pane's.
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

  // clickBarName clicks the centered title. Left toggles the tmux-style pane
  // zoom; right opens the rename input when the title is editable.
  async clickBarName(button: 'left' | 'right' = 'left'): Promise<void> {
    const b = await this.barName();
    await this.win.mouse.click(b.x + b.w / 2, b.top + b.height / 2, { button });
  }

  // leaveWorkspace exits down to workspace stack level toLevel; 0 is the
  // session. A crumb click goes to that crumb, so leaving means clicking the
  // last crumb before the pane-tile boundary of level toLevel+1: the outer
  // chain's tail. waitIdle covers the return animation, during which a click is
  // deliberately swallowed.
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

  // cellCenter maps a grid cell to screen coordinates and refuses a point
  // outside the pane's rect. An off-pane or off-viewport point is always a spec
  // bug: the cell is not where the viewport shows at this zoom. CDP silently
  // drops events dispatched outside the window, so half the gesture fires,
  // dragging never clears, and waitIdle hangs with no clue. Fail loudly with the
  // numbers instead.
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
  // any transition or fetch, and this waits for it to settle.
  async waitIdle(timeout = 20_000): Promise<void> {
    await this.win.waitForFunction(() => (window as any).__gridwellTest.idle(), null, { timeout });
  }

  // ── Gestures ────────────────────────────────────────────────────────────

  // enterPlugin puts the focused pane at the given plugin's root grid, matched
  // by kind or label. It is a no-op when the pane is already there; otherwise
  // the + menu's plugin swatch descends.
  async enterPlugin(match: string): Promise<void> {
    const pls = await this.plugins();
    const pl = pls.find((p) => p.kind === match || p.label === match);
    if (!pl) throw new Error(`no plugin matching ${match}; have ${pls.map((p) => p.kind)}`);
    const f = await this.focused();
    if (pl.rootGridID && f.gridID === pl.rootGridID) return; // already there
    await this.clickPluginSwatch(match);
  }

  // openPalette opens the focused pane's creation menu by clicking its + button;
  // the pane is already focused after a descent, so one click opens it. It
  // no-ops when the menu is already open, since the + click is a toggle and a
  // second open must not close it.
  async openPalette(): Promise<void> {
    const pal = await this.palette();
    if (pal.open) return;
    await this.win.mouse.click(pal.plusX, pal.plusY);
    await this.win.waitForFunction(() => (window as any).__gridwellTest.palette().open, null, {
      timeout: 5_000,
    });
  }

  // focusPane left-clicks the empty center of the given pane to move focus to
  // it. The grid is empty after an entry or split, so the press lands on no
  // tile: a pure focus click, with no descend and no pan. It exercises the
  // focus-change rules, such as the + menu closing when focus leaves its pane.
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

  // dragPluginLink drags a plugin swatch, matched by kind or label, onto cell
  // (cx, cy) of the focused pane: the drop-a-link gesture, distinct from
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
    await m.move(sx + 10, sy + 10); // past the 4px threshold: the drag arms
    await m.move(target.x, target.y, { steps: 8 });
    await m.up();
    await this.waitIdle();
  }

  // clickPaletteSwatch opens the palette and single-clicks, without dragging,
  // the primitive swatch of the given kind. A click differs from dragCreate's
  // drag: on the url swatch it opens the ephemeral-visit modal rather than
  // placing a tile.
  async clickPaletteSwatch(kind: string): Promise<void> {
    await this.openPalette();
    const pal = await this.palette();
    const item = pal.items.find((i) => !i.isPlugin && i.kind === kind);
    if (!item) throw new Error(`no palette primitive ${kind}; have ${pal.items.map((i) => i.kind)}`);
    await this.win.mouse.click(item.x + item.w / 2, item.y + item.h / 2);
  }

  // clickPluginSwatch opens the palette and single-clicks, without dragging, the
  // plugin swatch matched by kind or label: the descend-from-the-menu gesture.
  // The pane lands in the plugin's root grid.
  async clickPluginSwatch(match: string): Promise<void> {
    await this.openPalette();
    const pal = await this.palette();
    const item = pal.items.find((i) => i.isPlugin && (i.kind === match || i.label === match));
    if (!item) throw new Error(`no plugin swatch ${match}; have ${pal.items.map((i) => i.kind)}`);
    await this.win.mouse.click(item.x + item.w / 2, item.y + item.h / 2);
    await this.waitIdle();
  }

  // shellVisitURL fires the focused live shell's url-click path: the callback
  // xterm's link provider runs when a url in the terminal is clicked. A
  // terminal-cell link cannot be hit-tested from the canvas, so the e2e drives
  // it through this hook.
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

  // NUDGE is the first move of every synthetic drag: comfortably past the 4px
  // canvas and preload drag threshold, so the press reads as a drag.
  private static readonly NUDGE = 8;

  // dragTileCell left-drags the tile at cell (fromCx,fromCy) to (toCx,toCy) in
  // the focused pane: the move gesture. The tile's X and Y change on the server.
  async dragTileCell(fromCx: number, fromCy: number, toCx: number, toCy: number): Promise<void> {
    await this.dragCell(fromCx, fromCy, toCx, toCy, 'left');
  }

  // cloneTileCell right-drags from the center of the tile at (fromCx,fromCy) to
  // (toCx,toCy): the clone gesture, a right-drag from a tile's inner third. A
  // new independent tile lands at the destination cell.
  async cloneTileCell(fromCx: number, fromCy: number, toCx: number, toCy: number): Promise<void> {
    await this.dragCell(fromCx, fromCy, toCx, toCy, 'right');
  }

  // dragCell is the shared press, nudge, drag, release over two cell centers.
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

  // cloneDragAcrossPanes right-drags the tile at cell (fcx,fcy) of pane fromID
  // onto cell (tcx,tcy) of pane toID; a right-drag always copies. Across a
  // plugin boundary a leaf copies its bytes, a solid well is refused, and a link
  // tile copies as another link.
  async cloneDragAcrossPanes(fromID: string, fcx: number, fcy: number, toID: string, tcx: number, tcy: number): Promise<void> {
    await this.dragAcrossPanes(fromID, fcx, fcy, toID, tcx, tcy, 'right');
  }

  // leftDragAcrossPanes left-drags the tile at cell (fcx,fcy) of pane fromID
  // onto cell (tcx,tcy) of pane toID. Within one plugin this moves the tile;
  // across a plugin boundary it creates a link in the destination and the source
  // stays put. There is no cross-plugin move.
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

  // resizeTileCell right-drags from near the corner of the 1x1 tile at (cx,cy),
  // outside its center third so the gesture is a resize rather than a clone, out
  // to the center of (toCx,toCy). The tile's footprint rubber-bands to the
  // bounding box of the pinned corner and the cursor.
  async resizeTileCell(cx: number, cy: number, toCx: number, toCy: number): Promise<void> {
    const f = await this.focused();
    // About 0.35 cells past center toward the bottom-right corner: outside the
    // inner-third center zone of roughly +/-0.17, so right-down arms a resize.
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
  // button, which becomes a trashcan during a drag. The tile is removed on the
  // server.
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
  // threshold, drags to (toX,toY) and releases: the raw pane-gesture driver for
  // split, swap, and divider resize.
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
    // Start in the right-edge resize band, about 5px in, and drag to mid-pane.
    await this.rightDragScreen(p.x + p.w - 5, y, p.x + p.w * 0.45, y);
  }

  // splitFocusedPaneHorizontal right-drags inward from the focused pane's
  // bottom edge band, splitting it into two stacked panes.
  async splitFocusedPaneHorizontal(): Promise<void> {
    const p = await this.focused();
    // 30% across: the bottom edge band sits under the bar, where a right-down
    // falls through to the split gesture everywhere except the centered title,
    // which opens rename. So grab between the crumbs and the title.
    const x = p.x + p.w * 0.3;
    await this.rightDragScreen(x, p.y + p.h - 5, x, p.y + p.h * 0.45);
  }

  // hDividerGeom returns the midpoint of the horizontal boundary between two
  // stacked panes, which is the top pane's bottom edge.
  private async hDividerGeom(): Promise<{ x: number; y: number; topPaneH: number; topId: string }> {
    const ps = (await this.panes()).slice().sort((a, b) => a.y - b.y);
    if (ps.length < 2) throw new Error('hDividerGeom needs two panes');
    const top = ps[0];
    return { x: top.x + top.w / 2, y: top.y + top.h, topPaneH: top.h, topId: top.id };
  }

  // resizeHDivider drags the stacked-pane boundary by dy with the given button;
  // a positive dy grows the top pane. It returns the top pane's height before
  // and after.
  async resizeHDivider(
    button: 'left' | 'right',
    dy: number,
  ): Promise<{ before: number; after: number }> {
    const g = await this.hDividerGeom();
    // Grab from below the boundary, in the lower pane's top band: the upper side
    // can be the focused pane's bar band, which owns left-clicks. The two sides
    // of a border are otherwise equivalent.
    if (button === 'right') {
      await this.rightDragScreen(g.x, g.y + 2, g.x, g.y + dy);
    } else {
      await this.leftDragScreen(g.x, g.y + 2, g.x, g.y + dy);
    }
    const after = (await this.panes()).find((p) => p.id === g.topId);
    return { before: g.topPaneH, after: after ? after.h : 0 };
  }

  // leftDragScreen presses the left button at (fromX,fromY), nudges past the
  // threshold, drags to (toX,toY) and releases: the clamped left-drag
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

  // dividerGeom returns the x of the vertical boundary between the two
  // side-by-side panes, which is the left pane's right edge, and the shared
  // mid-height y.
  private async dividerGeom(): Promise<{ x: number; y: number; leftPaneW: number; leftId: string }> {
    const ps = (await this.panes()).slice().sort((a, b) => a.x - b.x);
    if (ps.length < 2) throw new Error('dividerGeom needs two panes');
    const left = ps[0];
    return { x: left.x + left.w, y: left.y + left.h / 2, leftPaneW: left.w, leftId: left.id };
  }

  // resizeDivider drags the pane divider by dx with the given button, shrinking
  // or collapsing the left pane. It returns the left pane's width before and
  // after.
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

  // ascendViaCrumb left-clicks the previous chain crumb in the focused pane's
  // bottom bar: the bar's ascent gesture.
  async ascendViaCrumb(): Promise<void> {
    const bar = await this.win.evaluate(() => (window as any).__gridwellTest.bar());
    const depth = await this.win.evaluate(() => (window as any).__gridwellTest.workspace().depth);
    // Only the current tree's crumbs: the one chain also carries the outer
    // context, and clicking those would cross the workspace boundary.
    const chain = (bar.segments as any[]).filter((s) => s.kind === 'chain' && s.level === depth);
    if (chain.length < 2) return; // nothing to ascend to
    const seg = chain[chain.length - 2];
    await this.win.mouse.click(seg.x + seg.w / 2, bar.top + bar.height / 2);
    // The ascent animates and input is blocked until it settles. Callers follow
    // this helper with an immediate next gesture, so settle here rather than at
    // every call site.
    await this.waitIdle();
  }

  // ── View gestures ─────────────────────────────────────────────────────────

  // middleClickPane middle-clicks the center of the focused pane's rect: the
  // universal ascend, and position-independent. Prefer it over middleClickCell
  // for a bare ascent, because a computed cell center can land outside the pane
  // at high zoom (one cell below center is off-pane once cells exceed half the
  // pane), and an off-pane click is swallowed without a trace.
  async middleClickPane(): Promise<void> {
    const f = await this.focused();
    await this.win.mouse.click(f.x + f.w / 2, f.y + f.h / 2, { button: 'middle' });
    await this.waitIdle();
  }

  // middleClickCell middle-clicks the center of a cell: the universal ascend
  // shortcut over a descended pane.
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

  // panFocusedGrid left-drags from one empty cell to another to pan the grid,
  // with no tile under the press point, shifting the viewport center.
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

  // typeText sends literal keystrokes to whatever has keyboard focus, such as a
  // text descent's editor, then waits for the debounced save to settle.
  async typeText(s: string): Promise<void> {
    await this.win.keyboard.type(s);
    await this.waitIdle();
  }


  // clickScreen single-left-clicks a raw screen coordinate.
  async clickScreen(x: number, y: number): Promise<void> {
    await this.win.mouse.click(x, y);
    await this.waitIdle();
  }

  // toggleTextMode left-clicks the bar-slot toggle of a file descent: the DOM
  // button that flips the focused pane between raw text and rendered markdown,
  // in the same slot the + button occupies on a grid pane.
  async toggleTextMode(): Promise<void> {
    const pal = await this.palette();
    await this.win.mouse.click(pal.plusX, pal.plusY);
    await this.waitIdle();
  }


  // textareaInfo returns the current textarea overlay binding: the pane it
  // covers, the tile it is bound to, and whether it has content. It returns null
  // when no pane is in raw-text mode. Specs use it to assert the overlay covers
  // exactly one pane, the focused descended one, and never a preview in another
  // pane.
  textareaInfo(): Promise<{ paneID: string; tileID: string; hasContent: boolean; x: number; y: number; w: number; h: number } | null> {
    return this.win.evaluate(() => (window as any).__gridwellTest.textareaInfo());
  }

  // textareaValue reads the raw-text overlay's current buffer straight from the
  // DOM: what the user sees in a text descent. It is null when no textarea
  // overlay exists.
  textareaValue(): Promise<string | null> {
    return this.win.evaluate(() => {
      const ta = document.querySelector<HTMLTextAreaElement>('#gw-text-editor');
      return ta ? ta.value : null;
    });
  }
}
