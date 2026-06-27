import type { Page } from '@playwright/test';
import { getGrid, GridSnapshot } from './oracle';

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
  cx: number;
  cy: number;
  zoom: number;
}

export interface PaletteItem {
  index: number;
  isPlugin: boolean;
  kind: string;
  label?: string;
  uuid?: string;
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

  // dragCreate drags the palette swatch of the given primitive kind ("well",
  // "markdown", "url", "shell") onto cell (cx, cy) of the focused pane: press on
  // the swatch, move past the 4px drag threshold, drag to the cell, release.
  async dragCreate(kind: string, cx: number, cy: number): Promise<void> {
    const pal = await this.palette();
    const item = pal.items.find((i) => !i.isPlugin && i.kind === kind);
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

  // descendCell single-clicks the center of cell (cx, cy) in the focused pane to
  // descend into the tile there, then waits for the zoom animation to settle.
  async descendCell(cx: number, cy: number): Promise<void> {
    const f = await this.focused();
    const c = await this.cellCenter(f.id, cx, cy);
    await this.win.mouse.click(c.x, c.y);
    await this.waitIdle();
  }

  // ── Oracle ──────────────────────────────────────────────────────────────

  getGrid(gridID: string): Promise<GridSnapshot> {
    return getGrid(this.origin, gridID);
  }
}
