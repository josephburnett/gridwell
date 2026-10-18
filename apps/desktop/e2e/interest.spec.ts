import { test, expect, homePassword, loginToken } from './fixtures';
import { tileAt } from './oracle';
import { dumpViaAPI, TraceLine } from './trace';

// The node's sources watch only what some client shows, and only the client
// knows what that is (pane.View, sent by the shim's syncView). The node's
// view/set and view/union records are its own account of what it heard, so
// this reads them across the whole seam: a gesture in the app, the view and
// the union on the node.

function lastUnion(lines: TraceLine[]): string[] | null {
  const u = lines.filter((l) => l.src === 'view' && l.kind === 'union');
  if (u.length === 0) return null;
  const msg = u[u.length - 1].msg;
  return msg === '' ? [] : msg.split(' ');
}

// focusedView is the last view the node heard: its label and the grid of its
// focused pane (viewLine marks it with a trailing *).
function focusedView(lines: TraceLine[]): string | null {
  const v = lines.filter((l) => l.src === 'view' && l.kind === 'set');
  if (v.length === 0) return null;
  const last = v[v.length - 1];
  const pane = last.msg.split(' ').find((w) => w.endsWith('*'));
  return `${last.kv?.label} ${pane ? pane.slice(pane.indexOf('=') + 1, -1) : ''}`;
}

test('the node hears which grids the panes and their previews show, and hears them leave', async ({ gw, home }) => {
  const token = await loginToken(gw.origin, homePassword(home));
  const union = async () => lastUnion(await dumpViaAPI(gw.origin, token));
  const view = async () => focusedView(await dumpViaAPI(gw.origin, token));

  await gw.enterPlugin('home');
  const f = await gw.focused();
  const rootGrid = f.gridID;
  await expect.poll(union, { message: 'the landing grid is shown', timeout: 10_000 }).toEqual([rootGrid]);
  await expect
    .poll(view, { message: 'the desktop view names its focused pane on the landing grid', timeout: 10_000 })
    .toBe(`desktop ${rootGrid}`);

  const wx = Math.round(f.cx);
  const wy = Math.round(f.cy);
  await gw.openMenu();
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
  await expect
    .poll(view, { message: 'the focused pane is at the grid it descended into', timeout: 10_000 })
    .toBe(`desktop ${child}`);

  await gw.ascendViaCrumb();
  await expect
    .poll(union, { message: 'ascending shows the landing grid and the preview again', timeout: 10_000 })
    .toEqual([child, rootGrid].sort());
});
