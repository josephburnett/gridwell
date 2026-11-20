import type { Page } from '@playwright/test';
import { test, expect } from './fixtures';
import { createText, getTile } from './oracle';
import type { PaneInfo } from './driver';

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
