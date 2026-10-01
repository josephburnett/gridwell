import { test, expect, homePassword, loginToken } from './fixtures';
import { tileAt } from './oracle';
import { dumpViaDoor, TraceLine } from './trace';

// The node's sources watch only what some client shows, and only the client
// knows what that is (pane.Showing, sent by the shim's syncInterest). The
// node's interest/union record is its own account of what it heard, so this
// reads it across the whole seam: a gesture in the app, the union on the node.

function lastUnion(lines: TraceLine[]): string[] | null {
  const u = lines.filter((l) => l.src === 'interest' && l.kind === 'union');
  if (u.length === 0) return null;
  const msg = u[u.length - 1].msg;
  return msg === '' ? [] : msg.split(' ');
}

test('the node hears which grids the panes and their previews show, and hears them leave', async ({ gw, home }) => {
  const token = await loginToken(gw.origin, homePassword(home));
  const union = async () => lastUnion(await dumpViaDoor(gw.origin, token));

  await gw.enterPlugin('home');
  const f = await gw.focused();
  const rootGrid = f.gridID;
  await expect.poll(union, { message: 'the landing grid is shown', timeout: 10_000 }).toEqual([rootGrid]);

  const wx = Math.round(f.cx);
  const wy = Math.round(f.cy);
  await gw.openPalette();
  await gw.dragCreate('well', wx, wy);
  const well = tileAt(await gw.getGrid(rootGrid), 'well', wx, wy);
  expect(well, 'the well persisted').toBeTruthy();

  const child = well!.childGridId!;
  await expect
    .poll(union, { message: "the well's preview shows its grid too", timeout: 10_000 })
    .toEqual([child, rootGrid].sort());

  await gw.descendCell(wx, wy);
  expect((await gw.focused()).gridID).toBe(child);
  await expect
    .poll(union, { message: 'descending swaps the grid shown', timeout: 10_000 })
    .toEqual([child]);

  await gw.ascendViaCrumb();
  await expect
    .poll(union, { message: 'ascending shows the landing grid and the preview again', timeout: 10_000 })
    .toEqual([child, rootGrid].sort());
});
