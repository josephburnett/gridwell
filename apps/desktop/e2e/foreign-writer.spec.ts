import { test, expect } from './fixtures';
import { tileAt, updateText } from './oracle';
import type { GridwellDriver } from './driver';

// The foreign-writer seam, end to end: another device edits a text tile this
// app is also looking at. Two contracts:
//
//  1. Visibility. The Subscribe event advances the tile row, and the cached
//     body must age with it, or the client renders stale bytes forever.
//  2. No stomp. A save claims the version its bytes derive from, so a save
//     based on a stale body conflicts at the server and reconciles instead of
//     riding the row version the foreign event advanced.

// createTextTile leaves a persisted markdown tile at the focused pane's center.
async function createTextTile(
  gw: GridwellDriver,
  seed: string,
): Promise<{ tileID: string; gridID: string; cx: number; cy: number }> {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  await gw.openMenu();
  await gw.dragCreate('markdown', cx, cy);
  const created = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;
  expect(created, 'markdown tile created').toBeTruthy();
  await gw.descendCell(cx, cy);
  await gw.typeText(seed);
  await gw.ascendViaCrumb();
  await expect
    .poll(async () => gw.getTileContent(created.id), { timeout: 10_000 })
    .toBe(seed);
  return { tileID: created.id, gridID: f.gridID, cx, cy };
}

async function serverVersion(gw: GridwellDriver, gridID: string, tileID: string): Promise<number> {
  const snap = await gw.getGrid(gridID);
  const t = (snap.tiles ?? []).find((n) => n.id === tileID);
  if (!t) throw new Error(`tile ${tileID} not on the server`);
  return Number((t as { version?: number | string }).version ?? 0);
}

test('a foreign edit becomes visible on re-descent', async ({ gw }) => {
  const { tileID, gridID, cx, cy } = await createTextTile(gw, 'written here');

  // The phone edits, directly on the server.
  await updateText(gw.origin, tileID, await serverVersion(gw, gridID, tileID), 'written elsewhere');
  await expect
    .poll(async () => gw.getTileContent(tileID), { timeout: 10_000 })
    .toBe('written elsewhere');

  // The Subscribe event drops the stale clean body, so the descent refetches
  // and the editor shows the foreign words.
  await gw.descendCell(cx, cy);
  await expect
    .poll(async () => gw.textareaValue(), { timeout: 10_000 })
    .toBe('written elsewhere');
  await gw.ascendViaCrumb();
});

test('opening and closing the tile never stomps a foreign edit', async ({ gw }) => {
  const { tileID, gridID, cx, cy } = await createTextTile(gw, 'written here');

  // This client is inside the tile, its buffer seeded from cache, when the
  // phone's edit lands on the server.
  await gw.descendCell(cx, cy);
  await updateText(gw.origin, tileID, await serverVersion(gw, gridID, tileID), 'phone words');
  await expect
    .poll(async () => gw.getTileContent(tileID), { timeout: 10_000 })
    .toBe('phone words');
  // The stomp needs the TileChanged event, which advances the row version the
  // flush would claim, so give the stream a beat. The assertion below is
  // meaningful either way.
  await new Promise((r) => setTimeout(r, 2000));

  // The ascent flush saves the stale buffer, claiming the version that buffer
  // derives from, so the server rejects it and the phone's words survive.
  await gw.ascendViaCrumb();

  // Poll long enough to catch a late stomp.
  for (let i = 0; i < 6; i++) {
    await new Promise((r) => setTimeout(r, 500));
    expect(await gw.getTileContent(tileID), 'foreign edit must survive open/close').toBe('phone words');
  }
});
