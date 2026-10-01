import { test, expect } from './fixtures';
import { tileAt } from './oracle';
import { settle } from './cadence';

// A draw asks for what it shows on every frame until it lands, and a read in
// flight answers every one of those asks. So a look reads each thing it shows
// once: a second read of the same grid is an ask that owed nothing, and a
// 60 fps transition makes one for everything in flight.

// askedIDs records the id field of each request to method.
async function askedIDs(window: any, method: string, field: string): Promise<string[]> {
  const asked: string[] = [];
  await window.route(`**/gridwell.v1.Gridwell/${method}`, async (r: any) => {
    asked.push(String(JSON.parse(r.request().postData() ?? '{}')[field] ?? ''));
    await r.continue();
  });
  return asked;
}

function count(asked: string[], id: string): number {
  return asked.filter((a) => a === id).length;
}

test('a descent and an ascent read each grid they show once', async ({ gw, window }) => {
  const settleMs = (await gw.cadences()).framingSaveMs;
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);

  // home ⊃ A ⊃ {B, C}: entering A draws B and C as wells, which reads their
  // grids.
  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  const a = tileAt(await gw.getGrid(home.gridID), 'well', cx, cy)!;
  await gw.descendCell(cx, cy);
  const inA = await gw.focused();
  const ax = Math.round(inA.cx);
  const ay = Math.round(inA.cy);
  await gw.openPalette();
  await gw.dragCreate('well', ax, ay);
  await gw.openPalette();
  await gw.dragCreate('well', ax + 1, ay);
  const inner = await gw.getGrid(a.childGridId!);
  const b = tileAt(inner, 'well', ax, ay)!;
  const c = tileAt(inner, 'well', ax + 1, ay)!;
  expect(b && c, 'A holds two wells').toBeTruthy();
  await gw.ascendViaCrumb();
  await settle(window, settleMs);

  // A reload empties the grid cache, so B's and C's grids are first read
  // inside A.
  const asked = await askedIDs(window, 'GetGrid', 'gridId');
  await window.reload();
  await window.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
  await expect.poll(async () => (await gw.focused()).gridID, { timeout: 30_000 }).toBe(home.gridID);
  await gw.waitIdle();
  await settle(window, settleMs);

  asked.length = 0;
  await gw.descendCell(cx, cy);
  expect((await gw.focused()).gridID).toBe(a.childGridId);
  await settle(window, settleMs);
  expect(count(asked, a.childGridId!), "the descent read A's grid").toBe(1);
  expect(count(asked, b.childGridId!), "B's grid, first shown inside A").toBe(1);
  expect(count(asked, c.childGridId!), "C's grid, first shown inside A").toBe(1);

  asked.length = 0;
  await gw.ascendViaCrumb();
  await settle(window, settleMs);
  expect(count(asked, home.gridID), 'the ascent read home').toBe(1);
  await window.unroute('**/gridwell.v1.Gridwell/GetGrid');
});
