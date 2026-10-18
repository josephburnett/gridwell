import { test, expect } from './fixtures';
import { tileAt } from './oracle';
import { settle } from './cadence';

// A draw asks for what it shows on every frame until it lands, and a read in
// flight answers every one of those asks. So a look reads each thing it shows
// once: a second read of the same grid is an ask that owed nothing, and a
// 60 fps transition makes one for everything in flight. Coming back to what the
// cache holds reads nothing: the event stream kept it true.

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

test('a boot, a descent and an ascent read each grid they show once', async ({ gw, window }) => {
  const settleMs = (await gw.cadences()).framingSaveMs;
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);

  // home ⊃ A ⊃ {B, C}: entering A draws B and C as doors, which reads their
  // grids.
  await gw.openMenu();
  await gw.dragCreate('door', cx, cy);
  const a = tileAt(await gw.getGrid(home.gridID), 'door', cx, cy)!;
  await gw.descendCell(cx, cy);
  const inA = await gw.focused();
  const ax = Math.round(inA.cx);
  const ay = Math.round(inA.cy);
  await gw.openMenu();
  await gw.dragCreate('door', ax, ay);
  await gw.openMenu();
  await gw.dragCreate('door', ax + 1, ay);
  const inner = await gw.getGrid(a.childGridId!);
  const b = tileAt(inner, 'door', ax, ay)!;
  const c = tileAt(inner, 'door', ax + 1, ay)!;
  expect(b && c, 'A holds two doors').toBeTruthy();
  await gw.ascendViaCrumb();
  await settle(window, settleMs);

  // A reload empties the grid cache, so the boot's restore walk and its first
  // draw both want home, and B's and C's grids are first read inside A.
  const asked = await askedIDs(window, 'GetGrid', 'gridId');
  await window.reload();
  await window.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
  await expect.poll(async () => (await gw.focused()).gridID, { timeout: 30_000 }).toBe(home.gridID);
  await gw.waitIdle();
  await settle(window, settleMs);
  expect(count(asked, home.gridID), 'the boot read home').toBe(1);

  asked.length = 0;
  await gw.descendCell(cx, cy);
  expect((await gw.focused()).gridID).toBe(a.childGridId);
  await settle(window, settleMs);
  // Home's draw read A's grid for its preview, so the descent holds it.
  expect(count(asked, a.childGridId!), "the descent read the A it holds").toBe(0);
  expect(count(asked, b.childGridId!), "B's grid, first shown inside A").toBe(1);
  expect(count(asked, c.childGridId!), "C's grid, first shown inside A").toBe(1);

  // Home is held and its source answers, so the event stream keeps it true.
  asked.length = 0;
  await gw.ascendViaCrumb();
  await settle(window, settleMs);
  expect(count(asked, home.gridID), 'the ascent read the home it holds').toBe(0);
  await window.unroute('**/gridwell.v1.Gridwell/GetGrid');
});

// A preview's bytes decode after the fetch that brought them has returned, so
// the frames between are a decoding blob, not a missing one.
test('a preview is fetched once on first look', async ({ electronApp, gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  // The ascent out of the live page freezes it, which mints the preview.
  await gw.openMenu();
  await gw.dragCreate('url', cx, cy);
  await gw.descendCell(cx, cy);
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?once=1`);
  await window.locator('#gw-url-form').evaluate((fm: HTMLFormElement) => fm.requestSubmit());
  const live = () =>
    electronApp.evaluate(({ webContents }) =>
      webContents.getAllWebContents().some((w) => w.getURL().includes('once=1')),
    );
  await expect.poll(live, { timeout: 15_000 }).toBe(true);
  await gw.middleClickCell(cx, cy);
  await expect.poll(live, { timeout: 15_000 }).toBe(false);
  await expect
    .poll(async () => Number(tileAt(await gw.getGrid(f.gridID), 'url', cx, cy)?.previewBlobId ?? 0), {
      timeout: 15_000,
    })
    .toBeGreaterThan(0);
  const tile = tileAt(await gw.getGrid(f.gridID), 'url', cx, cy)!;
  await gw.waitIdle();

  // The first look happens during an ascent, so frames are drawing while the
  // bytes decode: a reload inside a door empties the preview cache without
  // showing the tile, and the ascent out is stretched.
  await gw.openMenu();
  await gw.dragCreate('door', cx + 1, cy);
  await gw.descendCell(cx + 1, cy);
  const inside = (await gw.focused()).gridID;
  await settle(window, (await gw.cadences()).framingSaveMs);
  await window.reload();
  await window.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
  await expect.poll(async () => (await gw.focused()).gridID, { timeout: 30_000 }).toBe(inside);
  await gw.waitIdle();
  await window.evaluate(() => (window as any).__gridwellTest.setTransitionMs(1500));

  const asked = await askedIDs(window, 'GetTilePreview', 'tileId');
  await gw.ascendViaCrumb();
  await expect.poll(() => count(asked, tile.id), { timeout: 10_000 }).toBeGreaterThan(0);
  await gw.waitIdle();
  // Well past the decode: a second ask would have been a later frame's.
  await window.waitForTimeout(1_000);
  expect(count(asked, tile.id), 'GetTilePreview calls on first look').toBe(1);
  await window.unroute('**/gridwell.v1.Gridwell/GetTilePreview');
});

test('coming back to what the cache holds asks for nothing again', async ({ gw, window }) => {
  const settleMs = (await gw.cadences()).framingSaveMs;
  const depth = async () => (await window.evaluate(() => (window as any).__gridwellTest.workspace())).depth;
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy) - 2;
  await gw.openMenu();
  await gw.dragCreate('markdown', cx, cy);
  const text = tileAt(await gw.getGrid(home.gridID), 'text', cx, cy)!;
  const px = cx + Number(text.w ?? 1);
  await gw.openMenu();
  await gw.dragCreate('workspace', px, cy);
  const pt = tileAt(await gw.getGrid(home.gridID), 'workspace', px, cy);
  expect(pt, 'the workspace tile persisted').toBeTruthy();

  // One pane in the text tile below, one on home above.
  const top = home.id;
  await gw.ctrlDescendCell(cx, cy);
  const below = (await gw.focused()).id;
  expect(below).not.toBe(top);
  await gw.waitIdle();
  await settle(window, settleMs);

  const grids = await askedIDs(window, 'GetGrid', 'gridId');
  const tiles = await askedIDs(window, 'GetTile', 'tileId');

  // A first descent into a workspace tile captures the window, the text pane
  // included, and leaving it hands the outer tree back: a re-engagement of the
  // text pane each way.
  await gw.focusPane((await gw.panes()).find((p: any) => p.id === top)!);
  await gw.descendCell(px, cy);
  await expect.poll(depth).toBe(1);
  await gw.waitIdle();
  await settle(window, settleMs);
  await gw.leaveWorkspace();
  await expect.poll(depth).toBe(0);
  await gw.waitIdle();
  await settle(window, settleMs);

  // An ascent out of the text tile lands on home.
  await gw.focusPane((await gw.panes()).find((p: any) => p.id === below)!);
  await gw.ascendViaCrumb();
  await gw.waitIdle();
  await settle(window, settleMs);
  expect((await gw.panes()).find((p: any) => p.id === below)?.textFocus, 'the pane ascended').toBe('');

  expect.soft(count(grids, home.gridID), 'GetGrid of the home the cache held').toBe(0);
  expect(count(tiles, text.id), 'GetTile of the text row the cache held').toBe(0);
  expect(count(tiles, pt!.id), 'GetTile of the workspace tile row the cache held').toBe(0);
  await window.unroute('**/gridwell.v1.Gridwell/GetGrid');
  await window.unroute('**/gridwell.v1.Gridwell/GetTile');
});
