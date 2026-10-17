import { test, expect } from './fixtures';
import { setSelection, tileAt } from '../e2e/oracle';
import { installEchoGate, holdEchoFor } from './echo';
import type { GridwellDriver } from '../e2e/driver';
import type { Page } from '@playwright/test';

// A select is optimistic like a move (cache.Select): the border is the
// client's from the press, with every event held back. A select names the
// tiles it adds and removes and claims nothing, so another writer's select of
// another tile, made unseen, stands beside it.

const painted = async (window: Page, paneID: string): Promise<string[]> =>
  (((await window.evaluate(() => (window as any).__gridwellTest.selectionPainted()))[paneID] ?? []) as string[])
    .slice()
    .sort();

async function shiftClick(gw: GridwellDriver, window: Page, paneID: string, cx: number, cy: number) {
  const c = await gw.cellCenter(paneID, cx, cy);
  await window.keyboard.down('Shift');
  await window.mouse.click(c.x, c.y);
  await window.keyboard.up('Shift');
  await gw.waitIdle();
}

async function twoTiles(gw: GridwellDriver) {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  await gw.openPalette();
  await gw.dragCreate('markdown', cx + 2, cy);
  const snap = await gw.getGrid(f.gridID);
  const a = tileAt(snap, 'text', cx, cy)!;
  const b = tileAt(snap, 'text', cx + 2, cy)!;
  await gw.waitClientTileAt(f.id, cx + 2, cy, b.id);
  return { f, cx, cy, a, b };
}

test('selection: a select is the client\'s before its echo, and each next one lands', async ({ gw, window }) => {
  await installEchoGate(window);
  const { f, cx, cy, a, b } = await twoTiles(gw);

  await holdEchoFor(window, 60_000);
  await shiftClick(gw, window, f.id, cx, cy);
  expect(await painted(window, f.id), 'drawn from the press').toEqual([a.id]);
  await shiftClick(gw, window, f.id, cx + 2, cy);
  expect(await painted(window, f.id), 'the second select too').toEqual([a.id, b.id].sort());
  const onNode = (await gw.getGrid(f.gridID)).grid?.selection ?? [];
  expect(onNode.slice().sort(), 'both writes landed').toEqual([a.id, b.id].sort());

  await window.evaluate(() => ((window as any).__echoHeld = false));
  await gw.waitIdle();
  expect(await painted(window, f.id), 'the echoes change nothing').toEqual([a.id, b.id].sort());
});

test('selection: the user toggles one tile while another writer toggles another, and both land', async ({ gw, window, serve }) => {
  await installEchoGate(window);
  const { f, cx, cy, a, b } = await twoTiles(gw);

  // Another writer selects B; the client is not told.
  await holdEchoFor(window, 60_000);
  await setSelection(serve.origin, f.gridID, [b.id]);
  await shiftClick(gw, window, f.id, cx, cy);
  expect(await painted(window, f.id), 'the press draws A over the set the client last heard').toEqual([a.id]);
  await expect
    .poll(async () => ((await gw.getGrid(f.gridID)).grid?.selection ?? []).slice().sort(), {
      message: 'the node holds both selects',
    })
    .toEqual([a.id, b.id].sort());

  await window.evaluate(() => ((window as any).__echoHeld = false));
  await expect.poll(() => painted(window, f.id), { message: 'the echo brings B in beside A' }).toEqual([a.id, b.id].sort());
  const errs = await window.evaluate(() => (window as any).__gridwellTest.errors());
  expect(
    errs.notices.filter((n: any) => /SetSelection/.test(n.source) || /SetSelection/.test(n.message)),
    'nothing was refused',
  ).toEqual([]);

  // The other writer takes A out again, after the user put it in: the last
  // select of a tile wins.
  await setSelection(serve.origin, f.gridID, [], [a.id]);
  await expect.poll(() => painted(window, f.id), { message: 'the last select of A wins' }).toEqual([b.id]);
});
