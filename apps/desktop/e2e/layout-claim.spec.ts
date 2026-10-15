import { test, expect } from './fixtures';
import { tileAt, writeContent } from './oracle';
import { settle } from './cadence';

// A pane tile's layout is the user's bytes. Another window rearranges the
// workspace; this window's next layout write still claims the arrangement it
// read, so the node refuses it, and the window shows the newer arrangement
// rather than overwriting it.

// Four panes on the grid this window's own write anchored on, spelled as the
// client encodes them, so installing it writes nothing back.
function foreignLayout(anchor: string): string {
  const leaf = (id: string) => ({ pane: { id, anchor } });
  return JSON.stringify({
    v: 1,
    root: {
      split: {
        dir: 'v', ratio: 0.5,
        a: { split: { dir: 'h', ratio: 0.5, a: leaf('p1'), b: leaf('p2') } },
        b: { split: { dir: 'h', ratio: 0.5, a: leaf('p3'), b: leaf('p4') } },
      },
    },
    focus: 'p1',
  });
}

test('a stale layout write is refused and the workspace keeps the newer arrangement', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const wx = Math.round(f.cx);
  const wy = Math.round(f.cy);
  await gw.openPalette();
  await gw.dragCreate('pane', wx, wy);
  const pt = tileAt(await gw.getGrid(f.gridID), 'pane', wx, wy)!;
  expect(pt, 'the pane tile persisted').toBeTruthy();

  const c = await gw.cadences();
  await gw.descendCell(wx, wy);
  await gw.waitIdle();
  await settle(window, c.workspaceSaveMs);
  const before = await gw.getTileContent(pt.id);
  await gw.splitFocusedPaneVertical();
  await expect
    .poll(() => gw.getTileContent(pt.id), { message: "this window's own layout write landed", timeout: 10_000 })
    .not.toBe(before);
  const own = await gw.getTileContent(pt.id);
  const anchor = /"anchor":"([^"]+)"/.exec(own)?.[1];
  expect(anchor, `an anchor in ${own}`).toBeTruthy();
  await gw.waitIdle();
  expect((await gw.panes()).length).toBe(2);

  // Another window arranges four panes, claiming the version it read.
  const row = tileAt(await gw.getGrid(f.gridID), 'pane', wx, wy)!;
  const bytes = foreignLayout(anchor!);
  await writeContent(gw.origin, pt.id, Number(row.version ?? 0), Buffer.from(bytes));

  // This window splits again on the arrangement it read: refused, reconciled.
  await gw.splitFocusedPaneVertical();
  await expect
    .poll(async () => (await gw.panes()).length, { message: 'the window shows the arrangement on disk', timeout: 10_000 })
    .toBe(4);
  await gw.waitIdle();
  await settle(window, c.workspaceSaveMs);
  expect(await gw.getTileContent(pt.id), 'the newer arrangement was never overwritten').toBe(bytes);
  expect((await gw.panes()).length).toBe(4);
});
