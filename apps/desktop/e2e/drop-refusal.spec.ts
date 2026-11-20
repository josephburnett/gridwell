import * as fs from 'node:fs';
import * as path from 'node:path';
import type { Page } from '@playwright/test';
import { test, expect } from './fixtures';
import { makeRunDir } from './homes';
import { createText, getTile } from './oracle';
import type { GridwellDriver, PaneInfo } from './driver';

// Every refused drop says why, told and, where the ghost can know it,
// written under its no-entry badge (Joe, 2026-11-19: "any snap-back that is
// a denial of my request should say why"). A drop back on its own cell
// stays silent.

const notices = async (window: Page): Promise<string[]> =>
  ((await window.evaluate(() => (window as any).__gridwellTest.errors())).notices ?? []).map((n: any) => String(n.message));

// One notice per source, its count the reports it stands for.
const told = async (window: Page): Promise<[string, number][]> =>
  ((await window.evaluate(() => (window as any).__gridwellTest.errors())).notices ?? []).map((n: any) => [String(n.message), Number(n.count)]);

const ghostWhy = (window: Page): Promise<string> =>
  window.evaluate(() => (window as any).__gridwellTest.ghost().why);

test('a drop onto a taken cell is refused with the reason, and one on its own cell says nothing', async ({ gw, window }) => {
  const origin = new URL(window.url()).origin;
  await gw.enterSource('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  const a = await createText(origin, f.gridID, cx, cy, 'a');
  const b = await createText(origin, f.gridID, cx + 2, cy, 'b');
  await gw.waitClientTileAt(f.id, cx + 2, cy, b.id);
  const n = ((await gw.getGrid(f.gridID)).tiles ?? []).length;

  await gw.dragTileCell(cx, cy, cx, cy);
  expect(await notices(window), 'its own cell says nothing').toEqual([]);

  await gw.dragTileCell(cx, cy, cx + 2, cy);
  await expect.poll(() => told(window)).toEqual([['that cell is taken', 1]]);
  await gw.copyTileCell(cx, cy, cx + 2, cy);
  await expect.poll(() => told(window)).toEqual([['that cell is taken', 2]]);

  const at = await getTile(origin, a.id);
  expect([at.x, at.y], 'the tile did not move').toEqual([a.x, a.y]);
  expect(((await gw.getGrid(f.gridID)).tiles ?? []).length, 'nothing was made').toBe(n);
});

test('a drop onto a rendered text is refused with the reason, worn under the badge', async ({ gw, window }) => {
  const origin = new URL(window.url()).origin;
  await gw.enterSource('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  const text = await createText(origin, f.gridID, cx, cy, '# rendered\n');
  const moved = await createText(origin, f.gridID, cx + 1, cy, 'dragged');
  await gw.splitFocusedPaneVertical();
  const bId = (await gw.focused()).id;
  await gw.descendCell(cx, cy);
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).toBe(text.id);
  await expect.poll(() => gw.textareaValue(), { timeout: 10_000 }).toBe('# rendered\n');
  await gw.toggleTextMode();
  await expect.poll(async () => (await gw.focused()).textMode).toBe('rendered');
  const panes = await gw.panes();
  const b = panes.find((p: PaneInfo) => p.id === bId)!;
  const aPane = panes.find((p: PaneInfo) => p.id !== bId)!;
  await gw.focusPane(aPane);
  await gw.waitClientTileAt(aPane.id, cx + 1, cy, moved.id);
  const n = ((await gw.getGrid(f.gridID)).tiles ?? []).length;

  for (const button of ['left', 'right'] as const) {
    const from = await gw.cellCenter(aPane.id, cx + 1, cy);
    const m = window.mouse;
    await m.move(from.x, from.y);
    await m.down({ button });
    await m.move(from.x + 8, from.y + 8);
    await m.move(b.x + b.w / 2, b.y + b.h / 2, { steps: 10 });
    await expect.poll(() => ghostWhy(window), 'the ghost wears the reason').toBe('nothing here takes a drop');
    await m.up({ button });
    await gw.waitIdle();
  }
  await expect.poll(() => told(window)).toEqual([['nothing here takes a drop', 2]]);
  const at = await getTile(origin, moved.id);
  expect([at.gridId, at.x, at.y], 'the tile did not move').toEqual([moved.gridId, moved.x, moved.y]);
  expect(((await gw.getGrid(f.gridID)).tiles ?? []).length, 'nothing was made').toBe(n);
});

test('a swatch dropped onto a taken cell is refused with the reason', async ({ gw, window }) => {
  const origin = new URL(window.url()).origin;
  await gw.enterSource('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  const a = await createText(origin, f.gridID, cx, cy, 'a');
  await gw.waitClientTileAt(f.id, cx, cy, a.id);
  const n = ((await gw.getGrid(f.gridID)).tiles ?? []).length;

  await gw.openMenu();
  await gw.dragCreate('markdown', cx, cy);
  await expect.poll(() => notices(window)).toEqual(['that cell is taken']);
  expect(((await gw.getGrid(f.gridID)).tiles ?? []).length, 'nothing was made').toBe(n);
});

// A + menu swatch's refusals are blue notices too (Joe, 2026-11-20: "All the
// reasons should be blue notification bars at the bottoms.").
test.describe('a swatch refused for what it is', () => {
  const ROOT = makeRunDir();
  test.use({
    filesRoots: [{ root: ROOT, label: 'docs' }],
    extraYaml: `connections:
    - name: fixedcon
      label: rtb
      addr: 127.0.0.1:1
`,
  });

  // Two panes side by side, the left on home, the right on the root.
  async function homeAndDirectory(gw: GridwellDriver): Promise<{ home: PaneInfo; dir: PaneInfo }> {
    await gw.sources();
    await gw.splitFocusedPaneVertical();
    const [l0, r0] = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
    await gw.focusPane(l0);
    await gw.enterSource('home');
    await gw.focusPane((await gw.panes()).find((p) => p.id === r0.id)!);
    await gw.enterSource('docs (files)');
    const panes = await gw.panes();
    return { home: panes.find((p) => p.id === l0.id)!, dir: panes.find((p) => p.id === r0.id)! };
  }

  const count = async (gw: GridwellDriver, grid: string): Promise<number> => ((await gw.getGrid(grid)).tiles ?? []).length;

  test('a file swatch dropped on a Gridwell grid says a file is made in a directory', async ({ gw, window }) => {
    const { home, dir } = await homeAndDirectory(gw);
    const before = fs.readdirSync(ROOT).sort();
    const n = await count(gw, home.gridID);
    await gw.focusPane(dir);
    await gw.openMenu();
    const at = await gw.cellCenter(home.id, Math.round(home.cx), Math.round(home.cy));
    const item = (await gw.menu()).items.find((i) => !i.isSwatch && i.kind === 'file')!;
    const m = window.mouse;
    await m.move(item.x + item.w / 2, item.y + item.h / 2);
    await m.down();
    await m.move(item.x + item.w / 2 + 10, item.y + item.h / 2 + 10);
    await m.move(at.x, at.y, { steps: 8 });
    await expect.poll(() => ghostWhy(window), 'the ghost wears the reason').toBe('a file is made in a directory');
    await m.up();
    await gw.waitIdle();
    await expect.poll(() => told(window)).toEqual([['a file is made in a directory', 1]]);
    expect(await window.locator('#gw-url-modal.open').count(), 'no name is asked for').toBe(0);
    expect(await count(gw, home.gridID), 'nothing was made').toBe(n);
    expect(fs.readdirSync(ROOT).sort(), 'nothing on disk').toEqual(before);
  });

  test('a file swatch dropped on a gone directory says it cannot be written', async ({ gw, window }) => {
    const { dir } = await homeAndDirectory(gw);
    // A directory holding a Gridwell text, then removed underneath it.
    fs.mkdirSync(path.join(ROOT, 'held'));
    await expect
      .poll(async () => ((await gw.getGrid(dir.gridID)).tiles ?? []).some((t) => t.altText === 'held'))
      .toBe(true);
    const door = ((await gw.getGrid(dir.gridID)).tiles ?? []).find((t) => t.altText === 'held')!;
    await gw.focusPane(dir);
    await gw.descendCell(Number(door.x ?? 0), Number(door.y ?? 0));
    const inside = await gw.focused();
    const [tx, ty] = [Math.round(inside.cx), Math.round(inside.cy)];
    await gw.openMenu();
    await gw.dragCreate('markdown', tx, ty);
    await expect.poll(() => count(gw, inside.gridID)).toBe(1);
    fs.rmSync(path.join(ROOT, 'held'), { recursive: true, force: true });
    await expect.poll(async () => ((await gw.getGrid(inside.gridID)).grid as any)?.writable ?? false, { message: 'the grid is gone' }).toBe(false);

    // The left pane opens the root, where the file swatch is offered.
    const left = (await gw.panes()).find((p) => p.id !== dir.id)!;
    await gw.focusPane(left);
    await gw.enterSource('docs (files)');
    const before = fs.readdirSync(ROOT).sort();
    await gw.openMenu();
    const at = await gw.cellCenter(dir.id, tx + 1, ty);
    await gw.dragCreateToScreen('file', at.x, at.y);
    await expect.poll(() => told(window)).toEqual([['this directory cannot be written', 1]]);
    expect(await window.locator('#gw-url-modal.open').count(), 'no name is asked for').toBe(0);
    expect(await count(gw, inside.gridID), 'nothing was made').toBe(1);
    expect(fs.readdirSync(ROOT).sort(), 'nothing on disk').toEqual(before);
  });

  test('a swatch of a source that will not open says what its click says', async ({ gw, window }) => {
    await gw.enterSource('home');
    const f = await gw.focused();
    const n = await count(gw, f.gridID);
    await expect
      .poll(async () => (await gw.sources()).find((p) => p.label === 'rtb')?.status, { timeout: 30_000 })
      .toBe('broken');
    const rtb = (await gw.sources()).find((p) => p.label === 'rtb')!;
    await gw.openMenu();
    await gw.dragSourceLink('rtb', Math.round(f.cx), Math.round(f.cy));
    await expect.poll(() => told(window)).toEqual([[`rtb: ${rtb.infoError}`, 1]]);
    expect(await count(gw, f.gridID), 'nothing was made').toBe(n);
  });
});
