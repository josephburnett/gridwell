import type { Page } from '@playwright/test';
import { test, expect } from './fixtures';
import { tileAt } from './oracle';
import type { GridwellDriver } from './driver';

// A drop's landing belongs to the ghost it started on (anim.Flight): a tile
// grabbed while the previous drop is still landing keeps its own ghost for the
// whole drag, and its drop lands where it was released. The first drop is a
// snap-back, the longer landing, so the second press fits inside it.

function ghostActive(win: Page): Promise<boolean> {
  return win.evaluate(() => (window as any).__gridwellTest.ghost().active);
}

async function textTile(gw: GridwellDriver, cx: number, cy: number) {
  const f = await gw.focused();
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const t = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;
  expect(t, 'created text tile').toBeTruthy();
  await expect.poll(async () => (await gw.focused()).tileIds.includes(t.id)).toBe(true);
  return t;
}

test('a tile grabbed while the last drop lands keeps its ghost and lands where released', async ({
  gw,
  window,
}) => {
  const { snapBackMs } = await gw.cadences();
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  const a = await textTile(gw, cx, cy);
  const b = await textTile(gw, cx + 2, cy);
  // B's create lands too; A's grab waits it out so A's drop has a landing of
  // its own.
  await expect.poll(() => ghostActive(window)).toBe(false);

  const aFrom = await gw.cellCenter(f.id, cx, cy);
  const bFrom = await gw.cellCenter(f.id, cx + 2, cy);
  const bTo = await gw.cellCenter(f.id, cx + 2, cy - 2);

  await window.mouse.move(aFrom.x, aFrom.y);
  await window.mouse.down();
  await window.mouse.move(aFrom.x + 8, aFrom.y + 8);
  await window.mouse.move(aFrom.x - 60, aFrom.y, { steps: 4 });
  await window.mouse.move(aFrom.x, aFrom.y, { steps: 4 });
  const dropped = await window.evaluate(() => performance.now());
  await window.mouse.up();
  expect(await ghostActive(window), 'A is snapping back').toBe(true);
  // B's press and its ghost, inside A's landing.
  await window.mouse.move(bFrom.x, bFrom.y);
  await window.mouse.down();
  await window.mouse.move(bFrom.x + 8, bFrom.y + 8);
  const grabbed = await window.evaluate(() => performance.now());
  expect(grabbed - dropped, 'B was grabbed while A was landing').toBeLessThan(snapBackMs);
  expect(await ghostActive(window), 'B has a ghost').toBe(true);

  // Past A's landing, B is still being dragged and still has its ghost.
  await window.waitForTimeout(snapBackMs * 2);
  await window.mouse.move(bTo.x, bTo.y, { steps: 4 });
  expect(await ghostActive(window), 'A landing did not take B ghost').toBe(true);
  await window.mouse.up();
  await gw.waitIdle();

  const snap = await gw.getGrid(f.gridID);
  expect(tileAt(snap, 'text', cx, cy)?.id, 'A stayed in its cell').toBe(a.id);
  expect(tileAt(snap, 'text', cx + 2, cy - 2)?.id, 'B landed where released').toBe(b.id);
  await expect.poll(() => ghostActive(window), 'B landing ends its ghost').toBe(false);
});
