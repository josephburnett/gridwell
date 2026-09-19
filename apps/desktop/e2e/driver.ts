import type { Page } from '@playwright/test';
import { getGrid, getTileContent, GridSnapshot } from './oracle';

// GridwellDriver is the gesture layer the e2e tests are written against. It
// reads window.__gridwellTest, the renderer's read-only hook under ?e2e=1, for
// where to click and for the idle() settle signal, so a spec waits on state
// rather than on sleeps, and the server oracle (getGrid) for what a gesture
// created. Every click goes through window.mouse, which dispatches real CDP
// input the canvas listeners receive as they would a user's mouse.

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
  // The ids of the tiles this pane renders, which is its cache contents. A tile
  // the getGrid oracle sees but this does not has disappeared from the client.
  tileIds: string[];
}

// The waits client/cadence declares, in ms.
export interface Cadences {
  textSaveMs: number;
  urlUpdateMs: number;
  framingSaveMs: number;
  workspaceSaveMs: number;
  shellMirrorMs: number;
  traceFadeMs: number;
}

export interface PaletteItem {
  index: number;
  // Doorway swatches sit in the top row, one per declared doorway. A click
  // descends into it, a drag drops an exit-well link. isPlugin tells them from
  // the primitive swatches.
  isPlugin: boolean;
  kind: string;
  label?: string;
  uuid?: string;
  rootGridID?: string;
  status?: string;
  // The same selector the bar's crumb for this row's root grid wears, so a
  // spec can pin that the two agree.
  glyph?: string;
  // The declared menu entry this swatch came from, such as the home's "trash".
  // Empty on a row's own place.
  entry?: string;
  x: number;
  y: number;
  w: number;
  h: number;
}

// One configured plugin as the client knows it, from the position-free
// window.__gridwellTest.plugins(), available wherever the focused pane sits.
export interface PluginDescriptor {
  index: number;
  kind: string;
  label: string;
  uuid: string;
  // The row's own grid. A plugin names none of its own, so this is empty for
  // one and its grids are in menuEntries.
  rootGridID: string;
  menuEntries: PluginCollection[];
  // The scratch grid stamped on rootGridID, where ephemeral visits from it
  // land: empty until that grid is cached, so read it through scratchGridID().
  scratchGridID: string;
  infoError: string;
  status: string;
  // The handshake's persisted view of that grid; zoom 0 means never set. The
  // server-side oracle for a root-grid reframe.
  rootViewCx: number;
  rootViewCy: number;
  rootViewZoom: number;
}

// One declared menu entry: a grid the row is a doorway onto. label is instance
// and collection joined (door.EntryName).
export interface PluginCollection {
  id: string;
  label: string;
  gridID: string;
  viewCx: number;
  viewCy: number;
  viewZoom: number;
}

// The plugin section's disclosure strip. chevron is "up" collapsed, "down"
// expanded, "none" when there is no control.
export interface PaletteToggle {
  present: boolean;
  chevron: string;
  expanded: boolean;
  x: number;
  y: number;
  w: number;
  h: number;
}

export interface PaletteInfo {
  open: boolean;
  plusX: number;
  plusY: number;
  items: PaletteItem[];
  // The hovered swatch index, or -1, read off client/menu, which owns the
  // menu's live state.
  hover: number;
  toggle: PaletteToggle;
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

  // The client's named waits, in ms, straight off client/cadence. A spec
  // derives its own waits from these, so a retuned cadence retunes the spec
  // with it and a spec can never sleep less than the debounce it waits on.
  cadences(): Promise<Cadences> {
    return this.win.evaluate(() => (window as any).__gridwellTest.cadences());
  }

  // How many mirror passes the live-shell snapshotter has taken since boot.
  shellMirrors(): Promise<number> {
    return this.win.evaluate(() => (window as any).__gridwellTest.shellMirrors());
  }

  // Waits for Handshake to land.
  async plugins(): Promise<PluginDescriptor[]> {
    await this.win.waitForFunction(() => (window as any).__gridwellTest.plugins().length > 0, null, {
      timeout: 15_000,
    });
    return this.win.evaluate(() => (window as any).__gridwellTest.plugins());
  }

  // The pane ids holding per-pane client state (a.locals). A collapsed pane's
  // id must disappear here, which is the proof forgetPane ran.
  localPaneIds(): Promise<string[]> {
    return this.win.evaluate(() => (window as any).__gridwellTest.localPaneIds());
  }

  // Left-drags the divider hard to the left edge, crushing the left pane below
  // the close threshold so the release collapses it.
  async collapseLeftPane(): Promise<void> {
    const [left] = (await this.panes()).slice().sort((a, b) => a.x - b.x);
    const y = left.y + left.h / 2;
    await this.leftDragScreen(left.x + left.w - 2, y, left.x + 6, y);
  }

  palette(): Promise<PaletteInfo> {
    return this.win.evaluate(() => (window as any).__gridwellTest.palette());
  }

  // The one bar's drawn rectangle plus every segment's rect and identity. It
  // rides whichever pane has focus: left and width are that pane's span, top
  // the reserved full-width row it sits in.
  async bar(): Promise<{
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
      closeOnly?: boolean;
    }>;
  }> {
    return this.win.evaluate(() => (window as any).__gridwellTest.bar());
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

  // Left toggles the tmux-style pane zoom; right opens the rename input.
  async clickBarName(button: 'left' | 'right' = 'left'): Promise<void> {
    const b = await this.barName();
    await this.win.mouse.click(b.x + b.w / 2, b.top + b.height / 2, { button });
  }

  // Exits down to workspace stack level toLevel; 0 is the session. A crumb
  // click goes to that crumb, so leaving means clicking the outer chain's tail.
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

  // Refuses a point outside the pane's rect, which is a spec bug: the cell is
  // not where the viewport shows at this zoom. CDP silently drops events
  // outside the window, so half a gesture fires and waitIdle hangs with no clue.
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

  // Blocks until the renderer reports no transition, drag or in-flight fetch.
  // window.mouse dispatches synchronously into the wasm handlers, so by the
  // time mouse.up() resolves the handler has armed whatever this waits on.
  async waitIdle(timeout = 20_000): Promise<void> {
    await this.win.waitForFunction(() => (window as any).__gridwellTest.idle(), null, { timeout });
  }

  // ── Gestures ────────────────────────────────────────────────────────────

  // Puts the focused pane at the plugin's root grid, matched by kind or label.
  async enterPlugin(match: string): Promise<void> {
    const pls = await this.plugins();
    const pl = pls.find((p) => p.kind === match || p.label === match);
    if (!pl) throw new Error(`no plugin matching ${match}; have ${pls.map((p) => p.kind)}`);
    const f = await this.focused();
    if (pl.rootGridID && f.gridID === pl.rootGridID) return; // already there
    await this.clickPluginSwatch(match);
  }

  // The scratch grid of the plugin's root grid, once the client has that grid.
  async scratchGridID(match: string): Promise<string> {
    await this.win.waitForFunction(
      (m: string) =>
        ((window as any).__gridwellTest.plugins() as PluginDescriptor[]).some(
          (p) => (p.kind === m || p.label === m) && p.scratchGridID !== '',
        ),
      match,
      { timeout: 15_000 },
    );
    const pls = await this.plugins();
    return pls.find((p) => p.kind === match || p.label === match)!.scratchGridID;
  }

  // Clicks the focused pane's + button. It no-ops when the menu is already
  // open, since + is a toggle and a second open must not close it.
  async openPalette(): Promise<void> {
    const pal = await this.palette();
    if (pal.open) return;
    await this.win.mouse.click(pal.plusX, pal.plusY);
    await this.win.waitForFunction(() => (window as any).__gridwellTest.palette().open, null, {
      timeout: 5_000,
    });
  }

  // Clicks the chevron strip, the gesture every plugin swatch sits behind
  // because a menu opens collapsed. It no-ops when the section is already
  // shown, so a caller never has to ask which state it is in.
  async expandPlugins(): Promise<void> {
    // A remote pane's menu waits on the far node's plugin list, so wait rather
    // than read a menu that has not been told yet.
    await this.win.waitForFunction(
      () => {
        const p = (window as any).__gridwellTest.palette();
        return p.open && p.toggle && (p.toggle.expanded || p.toggle.present);
      },
      null,
      { timeout: 15_000 },
    );
    const pal = await this.palette();
    if (pal.toggle.expanded) return;
    const t = pal.toggle;
    await this.win.mouse.click(t.x + t.w / 2, t.y + t.h / 2);
    await this.win.waitForFunction(
      () => (window as any).__gridwellTest.palette().toggle.expanded,
      null,
      { timeout: 5_000 },
    );
  }

  // Left-clicks the pane's empty center, which after an entry or split lands on
  // no tile and so neither descends nor pans.
  async focusPane(p: PaneInfo): Promise<void> {
    await this.win.mouse.click(p.x + p.w / 2, p.y + p.h / 2);
    await this.waitIdle();
  }

  // Drags a primitive swatch ("well", "markdown", "url", "shell") onto a cell
  // of the focused pane.
  async dragCreate(kind: string, cx: number, cy: number): Promise<void> {
    const pal = await this.palette();
    const item = pal.items.find((i) => !i.isPlugin && i.kind === kind);
    if (!item) throw new Error(`no palette primitive ${kind}; have ${pal.items.map((i) => i.kind)}`);
    await this.dragSwatchToCell(item, cx, cy);
  }

  // Drops a link, where clickPluginSwatch descends.
  async dragPluginLink(match: string, cx: number, cy: number): Promise<void> {
    await this.expandPlugins();
    const pal = await this.palette();
    const item = pal.items.find((i) => i.isPlugin && (i.kind === match || i.label === match));
    if (!item) throw new Error(`no plugin swatch ${match}; have ${pal.items.map((i) => i.kind)}`);
    await this.dragSwatchToCell(item, cx, cy);
  }

  // For a drop no cell address can name, such as a point inside a pane
  // descended into a tile, where there is no grid to take a cell from.
  async dragCreateToScreen(kind: string, x: number, y: number): Promise<void> {
    const pal = await this.palette();
    const item = pal.items.find((i) => !i.isPlugin && i.kind === kind);
    if (!item) throw new Error(`no palette primitive ${kind}; have ${pal.items.map((i) => i.kind)}`);
    await this.dragSwatchToScreen(item, x, y);
  }

  private async dragSwatchToCell(item: PaletteItem, cx: number, cy: number): Promise<void> {
    const f = await this.focused();
    const target = await this.cellCenter(f.id, cx, cy);
    await this.dragSwatchToScreen(item, target.x, target.y);
  }

  private async dragSwatchToScreen(item: PaletteItem, x: number, y: number): Promise<void> {
    const sx = item.x + item.w / 2;
    const sy = item.y + item.h / 2;

    const m = this.win.mouse;
    await m.move(sx, sy);
    await m.down();
    await m.move(sx + 10, sy + 10); // past the 4px threshold: the drag arms
    await m.move(x, y, { steps: 8 });
    await m.up();
    await this.waitIdle();
  }

  // A click, not a drag: on the url swatch it opens the ephemeral-visit modal,
  // where dragCreate places a tile.
  async clickPaletteSwatch(kind: string): Promise<void> {
    await this.openPalette();
    const pal = await this.palette();
    const item = pal.items.find((i) => !i.isPlugin && i.kind === kind);
    if (!item) throw new Error(`no palette primitive ${kind}; have ${pal.items.map((i) => i.kind)}`);
    await this.win.mouse.click(item.x + item.w / 2, item.y + item.h / 2);
  }

  // A click, not a drag: the pane lands in the plugin's root grid.
  async clickPluginSwatch(match: string): Promise<void> {
    await this.openPalette();
    await this.expandPlugins();
    const pal = await this.palette();
    const item = pal.items.find((i) => i.isPlugin && (i.kind === match || i.label === match));
    if (!item) throw new Error(`no plugin swatch ${match}; have ${pal.items.map((i) => i.kind)}`);
    await this.win.mouse.click(item.x + item.w / 2, item.y + item.h / 2);
    await this.waitIdle();
  }

  // The callback xterm's link provider runs on a url click. A terminal-cell
  // link cannot be hit-tested from the canvas, so the e2e drives it here.
  async shellVisitURL(url: string): Promise<void> {
    await this.win.evaluate((u) => (window as any).__gridwellTest.shellVisitURL(u), url);
    await this.waitIdle();
  }

  // Blocks until the focused pane's terminal is attached to its PTY and tmux
  // has painted the attach. That paint CLEARS the screen, so anything written
  // into the terminal before it (shellFeed) is erased and never returns. A
  // command's own output is the proof: the PTY carries input only once
  // attached, and tmux paints before it reads. Matched on the output row, never
  // the echoed command line, which carries the marker too.
  async shellAttached(timeout = 30_000): Promise<void> {
    const marker = `gw-attached-${Math.random().toString(36).slice(2, 8)}`;
    await this.win.keyboard.type(`printf '%s\\n' ${marker}`);
    await this.win.keyboard.press('Enter');
    await this.win.waitForFunction(
      (m) =>
        ((window as any).__gridwellTest.shellText() as string)
          .split('\n')
          .some((l) => l.trim() === m),
      marker,
      { timeout },
    );
  }

  // Single-clicks a cell's center to descend into the tile there.
  async descendCell(cx: number, cy: number): Promise<void> {
    const f = await this.focused();
    const c = await this.cellCenter(f.id, cx, cy);
    await this.win.mouse.click(c.x, c.y);
    await this.waitIdle();
  }

  // descendCell with Control held, so the descent lands in a new pane split
  // below, which takes focus.
  async ctrlDescendCell(cx: number, cy: number): Promise<void> {
    const f = await this.focused();
    const c = await this.cellCenter(f.id, cx, cy);
    await this.win.keyboard.down('Control');
    await this.win.mouse.click(c.x, c.y);
    await this.win.keyboard.up('Control');
    await this.waitIdle();
  }

  // ── Tile gestures (left/right button drags over the canvas) ───────────────

  // The first move of every synthetic drag, past the 4px canvas and preload
  // threshold, so the press reads as a drag.
  private static readonly NUDGE = 8;

  // The move gesture: the tile's X and Y change on the server.
  async dragTileCell(fromCx: number, fromCy: number, toCx: number, toCy: number): Promise<void> {
    await this.dragCell(fromCx, fromCy, toCx, toCy, 'left');
  }

  // The clone gesture, a right-drag from a tile's inner third. A new
  // independent tile lands at the destination cell.
  async cloneTileCell(fromCx: number, fromCy: number, toCx: number, toCy: number): Promise<void> {
    await this.dragCell(fromCx, fromCy, toCx, toCy, 'right');
  }

  // The link gesture: ctrl flips the right button from copy to link, so a
  // reference lands at the destination. Ctrl is held across the press, which is
  // where the gesture classifies.
  async linkTileCell(fromCx: number, fromCy: number, toCx: number, toCy: number): Promise<void> {
    await this.dragCell(fromCx, fromCy, toCx, toCy, 'right', true);
  }

  // ctrl is held across the press, because the canvas reads the modifier off
  // the mousedown event.
  private async dragCell(fromCx: number, fromCy: number, toCx: number, toCy: number, button: 'left' | 'right', ctrl = false): Promise<void> {
    const f = await this.focused();
    const from = await this.cellCenter(f.id, fromCx, fromCy);
    const to = await this.cellCenter(f.id, toCx, toCy);
    const m = this.win.mouse;
    await m.move(from.x, from.y);
    if (ctrl) await this.win.keyboard.down('Control');
    try {
      await m.down({ button });
      await m.move(from.x + GridwellDriver.NUDGE, from.y + GridwellDriver.NUDGE);
      await m.move(to.x, to.y, { steps: 8 });
      await m.up({ button });
    } finally {
      if (ctrl) await this.win.keyboard.up('Control');
    }
    await this.waitIdle();
  }

  // A right-drag always copies. Across a plugin boundary a leaf copies its
  // bytes, a solid well is refused, and a link tile copies as another link.
  async cloneDragAcrossPanes(fromID: string, fcx: number, fcy: number, toID: string, tcx: number, tcy: number): Promise<void> {
    await this.dragAcrossPanes(fromID, fcx, fcy, toID, tcx, tcy, 'right');
  }

  // Within one plugin this moves the tile; across a plugin boundary it links,
  // because there is no cross-plugin move.
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

  // A right-drag from outside the tile's center third, so the gesture is a
  // resize rather than a clone.
  async resizeTileCell(cx: number, cy: number, toCx: number, toCy: number): Promise<void> {
    const f = await this.focused();
    // 0.35 cells past center: outside the inner-third zone of about +/-0.17.
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

  // Left-drags onto the focused pane's + button, which is a trashcan during a
  // drag.
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

  // The raw pane-gesture driver for split, swap and divider resize.
  async rightDragScreen(fromX: number, fromY: number, toX: number, toY: number): Promise<void> {
    const m = this.win.mouse;
    await m.move(fromX, fromY);
    await m.down({ button: 'right' });
    await m.move(fromX - GridwellDriver.NUDGE, fromY);
    await m.move(toX, toY, { steps: 10 });
    await m.up({ button: 'right' });
    await this.waitIdle();
  }

  // Right-drags inward from the pane's right edge band.
  async splitFocusedPaneVertical(): Promise<void> {
    const p = await this.focused();
    const y = p.y + p.h / 2;
    await this.rightDragScreen(p.x + p.w - 5, y, p.x + p.w * 0.45, y);
  }

  // Right-drags inward from the pane's bottom edge band.
  async splitFocusedPaneHorizontal(): Promise<void> {
    const p = await this.focused();
    // The one bar lives below every pane, so the whole bottom edge is the
    // pane's own border band.
    const x = p.x + p.w * 0.3;
    await this.rightDragScreen(x, p.y + p.h - 5, x, p.y + p.h * 0.45);
  }

  // The horizontal boundary between two stacked panes.
  private async hDividerGeom(): Promise<{ x: number; y: number; topPaneH: number; topId: string }> {
    const ps = (await this.panes()).slice().sort((a, b) => a.y - b.y);
    if (ps.length < 2) throw new Error('hDividerGeom needs two panes');
    const top = ps[0];
    return { x: top.x + top.w / 2, y: top.y + top.h, topPaneH: top.h, topId: top.id };
  }

  // A positive dy grows the top pane. Returns its height before and after.
  async resizeHDivider(
    button: 'left' | 'right',
    dy: number,
  ): Promise<{ before: number; after: number }> {
    const g = await this.hDividerGeom();
    // The two sides of a border are equivalent.
    if (button === 'right') {
      await this.rightDragScreen(g.x, g.y + 2, g.x, g.y + dy);
    } else {
      await this.leftDragScreen(g.x, g.y + 2, g.x, g.y + dy);
    }
    const after = (await this.panes()).find((p) => p.id === g.topId);
    return { before: g.topPaneH, after: after ? after.h : 0 };
  }

  // The clamped left-drag pane-boundary resize.
  async leftDragScreen(fromX: number, fromY: number, toX: number, toY: number): Promise<void> {
    const m = this.win.mouse;
    await m.move(fromX, fromY);
    await m.down({ button: 'left' });
    await m.move(fromX - GridwellDriver.NUDGE, fromY);
    await m.move(toX, toY, { steps: 10 });
    await m.up({ button: 'left' });
    await this.waitIdle();
  }

  // The vertical boundary between two side-by-side panes.
  private async dividerGeom(): Promise<{ x: number; y: number; leftPaneW: number; leftId: string }> {
    const ps = (await this.panes()).slice().sort((a, b) => a.x - b.x);
    if (ps.length < 2) throw new Error('dividerGeom needs two panes');
    const left = ps[0];
    return { x: left.x + left.w, y: left.y + left.h / 2, leftPaneW: left.w, leftId: left.id };
  }

  // Returns the left pane's width before and after.
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

  // The bar's ascent gesture: a click on the previous chain crumb.
  async ascendViaCrumb(): Promise<void> {
    const bar = await this.win.evaluate(() => (window as any).__gridwellTest.bar());
    const depth = await this.win.evaluate(() => (window as any).__gridwellTest.workspace().depth);
    // Only the current tree's crumbs; the outer context's would cross the
    // workspace boundary.
    const chain = (bar.segments as any[]).filter((s) => s.kind === 'chain' && s.level === depth);
    if (chain.length < 2) return; // nothing to ascend to
    const seg = chain[chain.length - 2];
    await this.win.mouse.click(seg.x + seg.w / 2, bar.top + bar.height / 2);
    // The ascent animates and input is blocked until it settles.
    await this.waitIdle();
  }

  // ── View gestures ─────────────────────────────────────────────────────────

  // The universal ascend, independent of position. Prefer it over
  // middleClickCell for a bare ascent, because a computed cell center can land
  // outside the pane at high zoom and an off-pane click is swallowed.
  async middleClickPane(): Promise<void> {
    const f = await this.focused();
    await this.win.mouse.click(f.x + f.w / 2, f.y + f.h / 2, { button: 'middle' });
    await this.waitIdle();
  }

  // The universal ascend shortcut over a descended pane.
  async middleClickCell(cx: number, cy: number): Promise<void> {
    const f = await this.focused();
    const c = await this.cellCenter(f.id, cx, cy);
    await this.win.mouse.click(c.x, c.y, { button: 'middle' });
    await this.waitIdle();
  }

  // Negative dy zooms in.
  async wheelAtFocusedCenter(dy: number): Promise<void> {
    const p = await this.focused();
    await this.win.mouse.move(p.x + p.w / 2, p.y + p.h / 2);
    await this.win.mouse.wheel(0, dy);
    await this.waitIdle();
  }

  // Left-drags between empty cells, with no tile under the press point.
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

  // Types into whatever has keyboard focus. The save is debounced and idle()
  // does not cover it, so a caller that needs the bytes on the server waits on
  // getTileContent, bounded by cadences().textSaveMs.
  async typeText(s: string): Promise<void> {
    await this.win.keyboard.type(s);
    await this.waitIdle();
  }


  async clickScreen(x: number, y: number): Promise<void> {
    await this.win.mouse.click(x, y);
    await this.waitIdle();
  }

  // The DOM button that flips a file descent between raw text and rendered
  // markdown, in the slot the + button occupies on a grid pane.
  async toggleTextMode(): Promise<void> {
    const pal = await this.palette();
    await this.win.mouse.click(pal.plusX, pal.plusY);
    await this.waitIdle();
  }


  // The textarea overlay's binding, or null when no pane is in raw-text mode.
  // Specs assert it covers exactly one pane, never a preview in another.
  textareaInfo(): Promise<{ paneID: string; tileID: string; hasContent: boolean; x: number; y: number; w: number; h: number } | null> {
    return this.win.evaluate(() => (window as any).__gridwellTest.textareaInfo());
  }

  // The raw-text overlay's buffer, which is what the user sees, or null.
  textareaValue(): Promise<string | null> {
    return this.win.evaluate(() => {
      const ta = document.querySelector<HTMLTextAreaElement>('#gw-text-editor');
      return ta ? ta.value : null;
    });
  }
}
