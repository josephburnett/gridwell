import { test, expect, homePassword, loginToken } from './fixtures';
import { tileAt } from './oracle';
import { settle } from './cadence';
import { dumpNow, TraceLine } from './trace';

// A pane layout write's own echo can reach the client before the write's
// reply. Told, not asked: the client holds the bytes it sent, so it never
// reads the tile back after its own write (cache.BeginWrite).

// The oracle reads the tile too; only a read the client asked is counted,
// joined by the request id both halves record.
function readsAfterOwnWrites(lines: TraceLine[], id: string): number {
  const asked = new Set(lines.filter((l) => l.origin === 'client' && l.kind === 'rpc').map((l) => l.kv?.req));
  const node = lines.filter((l) => l.origin === 'node' && l.kind === 'rpc' && l.kv?.id === id);
  const wrote = node.filter((l) => l.msg === 'WriteContent ok').map((l) => l.t!);
  return node.filter(
    (l) => l.msg === 'ReadContent start' && asked.has(l.kv?.req) && wrote.some((w) => l.t! >= w - 50 && l.t! - w < 1000),
  ).length;
}

test('a pane layout write is never read back by the client that wrote it', async ({ gw, home, window }) => {
  const token = await loginToken(gw.origin, homePassword(home));
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
  let writes = 0;
  for (let i = 0; i < 4; i++) {
    const before = await gw.getTileContent(pt.id);
    await gw.splitFocusedPaneVertical();
    await expect
      .poll(() => gw.getTileContent(pt.id), { message: `layout write ${i + 1} landed`, timeout: 10_000 })
      .not.toBe(before);
    writes++;
    await gw.waitIdle();
    await settle(window, c.workspaceSaveMs);
  }
  const { lines } = await dumpNow(window, gw.origin, token);
  const wrote = lines.filter((l) => l.origin === 'node' && l.msg === 'WriteContent ok' && l.kv?.id === pt.id);
  expect(wrote.length, 'the trace saw the layout writes').toBeGreaterThanOrEqual(writes);
  expect(readsAfterOwnWrites(lines, pt.id), 'ReadContent of the pane tile right after its own layout write').toBe(0);
});
