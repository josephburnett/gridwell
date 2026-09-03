import { test, expect } from './fixtures';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { createExitWell, tileAt } from './oracle';

// A link into a namespace this node does not declare is DEAD, and dead is a
// state, not an error. Removing a plugin from server.yaml, retiring a
// connection name, or an id that never resolved leaves link tiles pointing at
// nothing, and links going dead is just a thing that happens: the user should
// see what there is to throw away, not a strip of errors about it.
//
// The seam here runs from server.yaml, through the handshake roster, into the
// client's verdict (client/deadref) and out to the tile: no unit layer sees
// that composition, and the two halves of the behavior — the tile draws itself
// dead, and NOTHING is asked about it — are only observable together against a
// live server.
//
// "z9gonee" is a well-formed namespace segment (7-char lowercase base36,
// letter-leading) that names no plugin and no connection in the seeded home.
const GONE = 'z9gonee';

async function errors(window: any) {
  return window.evaluate(() => (window as any).__gridwellTest.errors());
}

async function deadLinks(window: any, gridID: string): Promise<string[]> {
  return window.evaluate(
    (gid: string) => (window as any).__gridwellTest.deadLinks(gid),
    gridID,
  );
}

test('a link into a namespace the node does not declare renders dead, quietly', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx) + 1;
  const cy = Math.round(f.cy) + 1;

  // Count every GetGrid the client issues from here on, so the "no fetch
  // storm" half of the claim is measured rather than assumed. The route is
  // pass-through: this observes, it does not inject a failure.
  const asked: string[] = [];
  await window.route('**/gridwell.v1.Gridwell/GetGrid', async (r: any) => {
    asked.push(r.request().postData() ?? '');
    await r.continue();
  });

  // Seed the dangling link directly on the server: a link well whose child
  // grid lives in a namespace the node has never heard of. That is what a
  // link left behind by a removed plugin looks like on disk.
  const dead = await createExitWell(gw.origin, f.gridID, `${GONE}/1`, 'old files', cx, cy);
  expect(dead.id, 'the server accepted the dangling reference').toBeTruthy();

  // The client must see it as a link and as dead.
  await expect
    .poll(async () => (await deadLinks(window, f.gridID)).length, { timeout: 15_000 })
    .toBe(1);
  const row = tileAt(await gw.getGrid(f.gridID), 'well', cx, cy)!;
  expect(row.reference, 'still a link — dashed, and deleting it only unlinks').toBe(true);
  expect(row.altText, 'the label survives: you can still see what it was').toBe('old files');

  // Quietly: no notice, no strip. This is the whole point of the change — the
  // grid unavailable error this state replaced landed here as an error row.
  await window.waitForTimeout(1_500);
  const e = await errors(window);
  expect(e.notices, 'a dead link raises no notice').toEqual([]);
  expect(e.stripH, 'and reserves no strip height').toBe(0);

  // And nothing was ever asked about it: not once, let alone once per frame.
  expect(
    asked.filter((body) => body.includes(GONE)),
    'a dead namespace is never asked for',
  ).toEqual([]);

  // Descending does nothing — quietly. The pane stays exactly where it is.
  const before = await gw.focused();
  await gw.descendCell(cx, cy);
  const after = await gw.focused();
  expect(after.gridID, 'a dead link is not a doorway').toBe(before.gridID);
  expect(after.placeDepth, 'no frame was pushed').toBe(before.placeDepth);
  expect((await errors(window)).notices, 'and the click says nothing either').toEqual([]);

  await window.unroute('**/gridwell.v1.Gridwell/GetGrid');

  // Throwing it away is the point: delete still works, and it is a link, so
  // the delete only unlinks — there is nothing on the far side to cascade to.
  await gw.deleteTileCell(cx, cy);
  await expect
    .poll(async () => tileAt(await gw.getGrid(f.gridID), 'well', cx, cy))
    .toBeUndefined();
  expect((await errors(window)).notices, 'and the delete is quiet too').toEqual([]);
});

// The boundary. A plugin the node DECLARES is alive whatever state it is in,
// so its link tiles must never grey — that is the health and stale machinery's
// business, and greying them would hide a source that is coming back.
const FS_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'gridwell-deadlink-'));

test.describe('a declared namespace is never dead', () => {
  test.use({ extraPlugins: [{ kind: 'fs', name: 'files', config: { root: FS_ROOT } }] });

  test('a link into a declared plugin stays alive', async ({ gw, window }) => {
    const pls = await gw.plugins();
    const files = pls.find((p) => p.label === 'files')!;
    expect(files.rootGridID, 'the fs plugin is declared and rooted').toBeTruthy();

    await gw.enterPlugin('home');
    const f = await gw.focused();
    const cx = Math.round(f.cx) + 1;
    const cy = Math.round(f.cy) + 1;
    await createExitWell(gw.origin, f.gridID, files.rootGridID, 'files', cx, cy);

    // Give the verdict every chance to fire wrongly before asserting it did
    // not: the grid has to land, and the tile has to be drawn.
    await expect
      .poll(async () => tileAt(await gw.getGrid(f.gridID), 'well', cx, cy)?.reference)
      .toBe(true);
    await window.waitForTimeout(1_000);
    expect(await deadLinks(window, f.gridID), 'a declared plugin is alive').toEqual([]);
  });
});
