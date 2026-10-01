import { test, expect, homePassword, loginToken } from './fixtures';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { makeRunDir } from './homes';
import { tileAt } from './oracle';
import { dumpViaDoor, TraceLine } from './trace';

// A plugin collection seen only as a well's preview is on screen, so its
// plugin watches it and a change on disk redraws the preview with no gesture.
// What is shown is pane.Showing, the wells' child grids included; the fs
// plugin opens a Watch only for the contexts the node's interest names.

const ROOT = makeRunDir();
test.use({ extraPlugins: [{ kind: 'fs', name: 'code', config: { root: ROOT } }] });

function lastUnion(lines: TraceLine[]): string[] {
  const u = lines.filter((l) => l.src === 'interest' && l.kind === 'union');
  const msg = u.length === 0 ? '' : u[u.length - 1].msg;
  return msg === '' ? [] : msg.split(' ');
}

test('a collection shown as a well preview redraws when its source changes', async ({ gw, home, window }) => {
  for (const f of fs.readdirSync(ROOT)) fs.rmSync(path.join(ROOT, f), { recursive: true });
  fs.writeFileSync(path.join(ROOT, 'one.txt'), 'one\n');
  const token = await loginToken(gw.origin, homePassword(home));

  await gw.enterPlugin('home');
  const f = await gw.focused();
  const wx = Math.round(f.cx);
  const wy = Math.round(f.cy);
  await gw.openPalette();
  await gw.dragPluginLink('code', wx, wy);
  const well = tileAt(await gw.getGrid(f.gridID), 'well', wx, wy)!;
  expect(well, 'the well onto the collection persisted').toBeTruthy();
  const child = well.childGridId!;

  const previewed = () =>
    window.evaluate((gid: string) => Object.keys((window as any).__gridwellTest.gridSigs(gid)).length, child);
  await expect.poll(previewed, { message: 'the preview drew the one file', timeout: 10_000 }).toBe(1);
  await expect
    .poll(async () => lastUnion(await dumpViaDoor(gw.origin, token)), {
      message: 'the node hears the previewed grid as shown',
      timeout: 10_000,
    })
    .toEqual([child, f.gridID].sort());

  fs.writeFileSync(path.join(ROOT, 'two.txt'), 'two\n');
  await expect
    .poll(previewed, { message: 'the new file reached the preview without a gesture', timeout: 10_000 })
    .toBe(2);
});
