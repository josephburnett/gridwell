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
  cx: number;
  cy: number;
  zoom: number;
  // The ids of the tiles this pane renders (its cache contents). A tile present
  // on the server (the getGrid oracle) but missing here is the "disappeared" bug.
  tileIds: string[];
}

export interface PaletteItem {
  index: number;
  kind: string;
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
}

export interface LauncherTile {
  index: number;
  kind: string;
  label: string;
  uuid: string;
  rootGridID: string;
  scratchGridID: string;
  // infoError/status are the client/pluginhealth classification (issue #47):
  // status is "enterable" | "broken" | "rootless"; infoError is the server's
  // Info-failure text, set only when status is "broken".
  infoError: string;
  status: string;
  x: number;
  y: number;
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

  launcher(): Promise<LauncherTile[]> {
    return this.win.evaluate(() => (window as any).__gridwellTest.launcher());
  }

  // localPaneIds returns the pane ids that currently hold per-pane client state
  // (a.locals). After a pane is collapsed its id must disappear here — the proof
  // that forgetPane tore the per-pane state down rather than orphaning it.
  localPaneIds(): Promise<string[]> {
    return this.win.evaluate(() => (window as any).__gridwellTest.localPaneIds());
  }

  // collapseLeftPane right-drags the divider of a two-pane split hard to the left
  // edge, pushing the left pane below the close threshold so it collapses away.
  async collapseLeftPane(): Promise<void> {
    const [left] = (await this.panes()).slice().sort((a, b) => a.x - b.x);
    const y = left.y + left.h / 2;
    await this.rightDragScreen(left.x + left.w - 2, y, left.x + 6, y);
  }

  palette(): Promise<PaletteInfo> {
    return this.win.evaluate(() => (window as any).__gridwellTest.palette());
  }

  cellCenter(paneID: string, cx: number, cy: number): Promise<{ x: number; y: number }> {
    return this.win.evaluate(
      ([id, x, y]) => (window as any).__gridwellTest.cellCenter(id, x, y),
      [paneID, cx, cy] as [string, number, number],
    );
  }

  // waitIdle blocks until the renderer reports no transition, drag, or in-flight
  // fetch. window.mouse dispatches events synchronously into the wasm handlers,
  // so by the time a gesture's mouse.up() resolves the handler has already armed
  // any transition/fetch — this then waits for it to settle.
  async waitIdle(timeout = 20_000): Promise<void> {
    await this.win.waitForFunction(() => (window as any).__gridwellTest.idle(), null, { timeout });
  }

  // ── Gestures ────────────────────────────────────────────────────────────

  // enterPlugin clicks a launcher plugin tile (single click → descent) and
  // waits for the entry animation to settle. Matches by kind or label.
  async enterPlugin(match: string): Promise<void> {
    // The hook installs before ListPlugins resolves, so the launcher can be
    // momentarily empty — wait for the plugin tiles to appear first.
    await this.win.waitForFunction(() => (window as any).__gridwellTest.launcher().length > 0, null, {
      timeout: 15_000,
    });
    const tiles = await this.launcher();
    const t = tiles.find((x) => x.kind === match || x.label === match);
    if (!t) throw new Error(`no launcher plugin matching ${match}; have ${tiles.map((x) => x.kind)}`);
    await this.win.mouse.click(t.x, t.y);
    await this.waitIdle();
  }

  // openPalette opens the focused pane's creation menu by clicking its + button
  // (the pane is already focused after a descent, so a single click opens it).
  async openPalette(): Promise<void> {
    const pal = await this.palette();
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
    const item = pal.items.find((i) => i.kind === kind);
    if (!item) throw new Error(`no palette primitive ${kind}; have ${pal.items.map((i) => i.kind)}`);
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
    const item = pal.items.find((i) => i.kind === kind);
    if (!item) throw new Error(`no palette primitive ${kind}; have ${pal.items.map((i) => i.kind)}`);
    await this.win.mouse.click(item.x + item.w / 2, item.y + item.h / 2);
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

  // DRAG_THRESHOLD mirrors the canvas + preload threshold (client/wasm,
  // viewutil.ts): a press must move this far to become a drag rather than a click.
  private static readonly NUDGE = 8; // > the 4px threshold

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

  // cloneDragAcrossPanes right-drags (clone/link gesture) the tile at cell
  // (fcx,fcy) of pane fromID onto cell (tcx,tcy) of pane toID. Within one
  // plugin this clones; across a plugin boundary it creates a LINK in the
  // destination (a well) or a byte copy (a leaf).
  async cloneDragAcrossPanes(fromID: string, fcx: number, fcy: number, toID: string, tcx: number, tcy: number): Promise<void> {
    const from = await this.cellCenter(fromID, fcx, fcy);
    const to = await this.cellCenter(toID, tcx, tcy);
    const m = this.win.mouse;
    await m.move(from.x, from.y);
    await m.down({ button: 'right' });
    await m.move(from.x + GridwellDriver.NUDGE, from.y + GridwellDriver.NUDGE);
    await m.move(to.x, to.y, { steps: 10 });
    await m.up({ button: 'right' });
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
    const x = p.x + p.w / 2;
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
    if (button === 'right') {
      await this.rightDragScreen(g.x, g.y - 2, g.x, g.y + dy);
    } else {
      await this.leftDragScreen(g.x, g.y - 2, g.x, g.y + dy);
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

  // rightClickPlus right-presses-and-releases on the focused pane's corner
  // circle (the +/back button) without dragging — the discoverable ascend
  // gesture (release inside the circle ascends).
  async rightClickPlus(): Promise<void> {
    const pal = await this.palette();
    const m = this.win.mouse;
    await m.move(pal.plusX, pal.plusY);
    await m.down({ button: 'right' });
    await m.up({ button: 'right' });
    await this.waitIdle();
  }

  // ── View gestures ─────────────────────────────────────────────────────────

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

  // pressKey sends one named key (e.g. 'Enter') to whatever has keyboard focus.
  async pressKey(key: string): Promise<void> {
    await this.win.keyboard.press(key);
    await this.waitIdle();
  }

  // clickScreen single-left-clicks a raw screen coordinate (e.g. a point inside
  // a descended text tile's inner box, from textInnerBox()).
  async clickScreen(x: number, y: number): Promise<void> {
    await this.win.mouse.click(x, y);
    await this.waitIdle();
  }

  // toggleTextMode left-clicks the corner circle of a file descent — the DOM
  // toggle button that flips the focused pane between raw text and rendered
  // markdown (the same spot the + button occupies on a grid pane).
  async toggleTextMode(): Promise<void> {
    const pal = await this.palette();
    await this.win.mouse.click(pal.plusX, pal.plusY);
    await this.waitIdle();
  }

  // renderedCaret reads the focused pane's rendered-mode caret: the source byte
  // offset the next keystroke will splice at, or has=false when none is placed.
  renderedCaret(): Promise<{ has: boolean; offset: number }> {
    return this.win.evaluate(() => (window as any).__gridwellTest.renderedCaret());
  }

  // textInnerBox returns the screen rect the focused pane's file content is
  // rendered into (null when not descended into a file) — where to click to
  // hit the rendered text.
  textInnerBox(): Promise<{ x: number; y: number; w: number; h: number } | null> {
    return this.win.evaluate(() => (window as any).__gridwellTest.textInnerBox());
  }

  // textareaInfo returns the current textarea overlay binding: the pane it
  // covers, the tile it's bound to, and whether it has content (textareaReady).
  // Returns null when no pane is in raw-text mode. Used to assert the overlay
  // covers exactly one pane (the focused descended one), not preview nodes in
  // other panes — the issue #35 mechanism B invariant.
  textareaInfo(): Promise<{ paneID: string; tileID: string; hasContent: boolean; x: number; y: number; w: number; h: number } | null> {
    return this.win.evaluate(() => (window as any).__gridwellTest.textareaInfo());
  }
}
