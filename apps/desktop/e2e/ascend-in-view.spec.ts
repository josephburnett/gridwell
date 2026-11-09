import { test, expect } from './fixtures';
import { tileAt } from './oracle';
import { settle } from './cadence';

// An ascent lands with the door it came through in view, its center in the
// middle third of the pane, even when the pane has changed shape since the
// grid was left: a narrower pane at the grid's old center would not show it.
// The landing is not the user framing the grid, so nothing is written (Joe
// 2026-11-09, zoomtrans.InView).
test('an ascent into a narrower pane keeps the door in view and writes no framing', async ({
  gw,
  window,
}) => {
  const c = await gw.cadences();
  await gw.enterSource('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);
  const a = await gw.cellCenter(home.id, cx, cy);
  const b = await gw.cellCenter(home.id, cx + 1, cy);
  const cellPx = (b.x - a.x) / home.zoom;
  const half = home.w / 2 / (cellPx * home.zoom);
  // Far right of center, wholly in the wide pane.
  const dx = Math.round(home.cx + half * 0.6);
  await gw.openMenu();
  await gw.dragCreate('door', dx, cy);
  const row = tileAt(await gw.getGrid(home.gridID), 'door', dx, cy)!;
  const door = {
    x: Number(row.x ?? 0),
    y: Number(row.y ?? 0),
    w: Number(row.w ?? 0),
    h: Number(row.h ?? 0),
    childGridId: row.childGridId,
  };
  expect(door.x + door.w, 'the door fits the wide pane').toBeLessThan(home.cx + half);

  await gw.descendCell(dx, cy);
  await expect.poll(async () => (await gw.focused()).gridID).toBe(door.childGridId);
  await gw.waitIdle();
  await settle(window, c.framingSaveMs);
  const writes = () =>
    window.evaluate(() => Number((window as any).__gridwellTest.persistPosts().SetFraming ?? 0));
  const before = await writes();

  await gw.splitFocusedPaneVertical();
  const narrow = await gw.focused();
  expect(narrow.w, 'the split narrowed the pane').toBeLessThan(home.w * 0.7);
  const narrowHalf = narrow.w / 2 / (cellPx * home.zoom);
  expect(door.x + door.w / 2 - home.cx, 'the old center would not show the door').toBeGreaterThan(narrowHalf);

  await gw.middleClickPane();
  await gw.waitIdle();
  const landed = await gw.focused();
  expect(landed.gridID).toBe(home.gridID);
  const cell = cellPx * landed.zoom;
  const left = landed.x + landed.w / 2 + (door.x - landed.cx) * cell;
  const top = landed.y + landed.h / 2 + (door.y - landed.cy) * cell;
  const right = left + door.w * cell;
  const bottom = top + door.h * cell;
  expect(left, 'door left edge in view').toBeGreaterThanOrEqual(landed.x - 0.5);
  expect(right, 'door right edge in view').toBeLessThanOrEqual(landed.x + landed.w + 0.5);
  expect(top, 'door top edge in view').toBeGreaterThanOrEqual(landed.y - 0.5);
  expect(bottom, 'door bottom edge in view').toBeLessThanOrEqual(landed.y + landed.h + 0.5);
  const mid = (left + right) / 2;
  expect(mid, 'door center a third in').toBeLessThanOrEqual(landed.x + (landed.w * 2) / 3 + 0.5);
  expect(mid, 'door center a third in').toBeGreaterThanOrEqual(landed.x + landed.w / 3 - 0.5);
  expect(landed.zoom, 'the zoom is kept').toBeCloseTo(home.zoom, 6);

  await settle(window, c.framingSaveMs);
  expect(await writes(), 'the landing wrote no framing').toBe(before);
});
