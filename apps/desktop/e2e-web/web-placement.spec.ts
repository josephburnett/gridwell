import { test, expect } from './fixtures';
import { placeTile, tileAt } from '../e2e/oracle';
import { installEchoGate, holdEchoFor } from './echo';

// A placement is optimistic like every other write: the client's cache, which
// the next press hits, holds a move or a resize from the release, with every
// event held back, and a refused one is back where it was. See cache.Place.

const clientTileAt = (window: any, paneID: string, x: number, y: number): Promise<string> =>
  window.evaluate(
    ([id, cx, cy]: [string, number, number]) => (window as any).__gridwellTest.tileAt(id, cx, cy),
    [paneID, x, y],
  );

test('placement: a move and a resize are the client\'s before their echo', async ({ gw, window }) => {
  await installEchoGate(window);
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const t = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;
  expect(t, 'markdown tile created').toBeTruthy();
  await gw.waitClientTileAt(f.id, cx, cy, t.id!);

  await holdEchoFor(window, 60_000);
  await gw.dragTileCell(cx, cy, cx + 2, cy);
  expect(tileAt(await gw.getGrid(f.gridID), 'text', cx + 2, cy), 'the node moved it').toBeTruthy();
  expect(await clientTileAt(window, f.id, cx + 2, cy), 'the client holds the move').toBe(t.id);
  expect(await clientTileAt(window, f.id, cx, cy), 'and not the origin').toBe('');

  await gw.resizeTileCell(cx + 2, cy, cx + 3, cy + 1);
  expect(await clientTileAt(window, f.id, cx + 3, cy + 1), 'the client holds the resize').toBe(t.id);

  // The echoes are the same facts and change nothing.
  await window.evaluate(() => ((window as any).__echoHeld = false));
  await gw.waitIdle();
  expect(await clientTileAt(window, f.id, cx + 3, cy + 1)).toBe(t.id);
  expect(await clientTileAt(window, f.id, cx, cy)).toBe('');
});

test('placement: a refused move goes back and says so', async ({ gw, window, serve }) => {
  await installEchoGate(window);
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy + 2);
  const snap = await gw.getGrid(f.gridID);
  const a = tileAt(snap, 'text', cx, cy)!;
  const b = tileAt(snap, 'text', cx, cy + 2)!;
  await gw.waitClientTileAt(f.id, cx, cy + 2, b.id!);

  // Another writer takes the cell; the client is not told.
  await holdEchoFor(window, 60_000);
  await placeTile(serve.origin, b.id!, undefined, f.gridID, cx + 2, cy, 1, 1);
  await gw.dragTileCell(cx, cy, cx + 2, cy);

  expect(await clientTileAt(window, f.id, cx, cy), 'the refused move is back').toBe(a.id);
  expect(await clientTileAt(window, f.id, cx + 2, cy), 'the cell shows who took it').toBe(b.id);
  const errs = await window.evaluate(() => (window as any).__gridwellTest.errors());
  expect(
    errs.notices.some((n: any) => /PlaceTile/.test(n.source) || /PlaceTile/.test(n.message)),
    `the refusal is said: ${JSON.stringify(errs.notices)}`,
  ).toBe(true);
});
