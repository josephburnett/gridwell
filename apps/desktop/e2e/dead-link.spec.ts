import { test, expect } from './fixtures';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { createExitWell, tileAt } from './oracle';

// A link into a namespace this node does not declare is DEAD. Removing a
// plugin from server.yaml, retiring a connection name, or an id that never
// resolved leaves link tiles pointing at nothing. Dead is a state and raises
// nothing: the user sees what there is to throw away.
//
// The seam runs from server.yaml, through the handshake roster, into the
// client's verdict (client/deadref) and out to the tile. Its two halves, the
// tile draws itself dead and NOTHING is asked about it, are only observable
// together against a live server.
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

  // Count every GetGrid the client issues from here on, so the "nothing is
  // asked" half is measured. The route is pass-through and injects no failure.
  const asked: string[] = [];
  await window.route('**/gridwell.v1.Gridwell/GetGrid', async (r: any) => {
    asked.push(r.request().postData() ?? '');
    await r.continue();
  });

  // Seed the dangling link directly on the server: a link well whose child
  // grid lives in a namespace the node has never heard of. That is what a link
  // left behind by a removed plugin looks like on disk.
  const dead = await createExitWell(gw.origin, f.gridID, `${GONE}/1`, 'old files', cx, cy);
  expect(dead.id, 'the server accepted the dangling reference').toBeTruthy();

  // The client must see it as a link and as dead.
  await expect
    .poll(async () => (await deadLinks(window, f.gridID)).length, { timeout: 15_000 })
    .toBe(1);
  const row = tileAt(await gw.getGrid(f.gridID), 'well', cx, cy)!;
  expect(row.reference, 'still a link — dashed, and deleting it only unlinks').toBe(true);
  expect(row.altText, 'the label survives: you can still see what it was').toBe('old files');

  // Give a notice time to appear before asserting none did.
  await window.waitForTimeout(1_500);
  const e = await errors(window);
  expect(e.notices, 'a dead link raises no notice').toEqual([]);
  expect(e.stripH, 'and reserves no strip height').toBe(0);

  expect(
    asked.filter((body) => body.includes(GONE)),
    'a dead namespace is never asked for',
  ).toEqual([]);

  // Descending does nothing and says nothing.
  const before = await gw.focused();
  await gw.descendCell(cx, cy);
  const after = await gw.focused();
  expect(after.gridID, 'a dead link is not a doorway').toBe(before.gridID);
  expect(after.placeDepth, 'no frame was pushed').toBe(before.placeDepth);
  expect((await errors(window)).notices, 'and the click says nothing either').toEqual([]);

  await window.unroute('**/gridwell.v1.Gridwell/GetGrid');

  // Delete still works, and it is a link, so the delete only unlinks. There is
  // nothing on the far side to cascade to.
  await gw.deleteTileCell(cx, cy);
  await expect
    .poll(async () => tileAt(await gw.getGrid(f.gridID), 'well', cx, cy))
    .toBeUndefined();
  expect((await errors(window)).notices, 'and the delete is quiet too').toEqual([]);
});

// The boundary. A plugin the node DECLARES is alive whatever state it is in,
// so its link tiles must never grey. Health covers a declared source that is
// down, and greying it would hide one that is coming back.
const FS_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'gridwell-deadlink-'));

test.describe('a declared namespace is never dead', () => {
  test.use({ extraPlugins: [{ kind: 'fs', name: 'files', config: { root: FS_ROOT } }] });

  test('a link into a declared plugin stays alive', async ({ gw, window }) => {
    const pls = await gw.plugins();
    const files = pls.find((p) => p.label === 'files')!;
    // A plugin names no grid of its own, so the link goes to a collection it
    // declares.
    const collection = files.menuEntries[0]?.gridID;
    expect(collection, 'the fs plugin is declared and serves a collection').toBeTruthy();

    await gw.enterPlugin('home');
    const f = await gw.focused();
    const cx = Math.round(f.cx) + 1;
    const cy = Math.round(f.cy) + 1;
    await createExitWell(gw.origin, f.gridID, collection!, 'files', cx, cy);

    // Wait for the tile to be drawn, so the verdict has every chance to fire
    // wrongly before asserting it did not.
    await expect
      .poll(async () => tileAt(await gw.getGrid(f.gridID), 'well', cx, cy)?.reference)
      .toBe(true);
    await window.waitForTimeout(1_000);
    expect(await deadLinks(window, f.gridID), 'a declared plugin is alive').toEqual([]);
  });
});
