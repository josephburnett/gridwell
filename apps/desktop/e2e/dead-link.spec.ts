import * as fs from 'node:fs';
import * as path from 'node:path';
import { test, expect } from './fixtures';
import { createExitWell, createLeafLink, placeTile, tileAt } from './oracle';
import { makeRunDir } from './homes';

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
const FS_ROOT = makeRunDir();

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

  // The last hop answers, and the key is not there any more: the path ends in
  // nothing just the same. Reading the link's body hears the dead verdict,
  // draws it dead, and says nothing; the same id reads again once the file is
  // back and the namespace says a listing changed.
  test('a link to a file that is gone renders dead, quietly, until it is back', async ({ gw, window }) => {
    const file = path.join(FS_ROOT, 'gone.md');
    fs.writeFileSync(file, '# gone\nsoon\n');
    const files = (await gw.plugins()).find((p) => p.label === 'files')!;
    const collection = files.menuEntries[0]!.gridID;
    const entry = ((await gw.getGrid(collection)).tiles ?? []).find((t) => t.altText === 'gone.md');
    expect(entry, 'the file is listed').toBeTruthy();
    fs.rmSync(file);

    await gw.enterPlugin('home');
    const f = await gw.focused();
    const cx = Math.round(f.cx) + 1;
    const cy = Math.round(f.cy) + 1;
    await createLeafLink(gw.origin, f.gridID, entry!.id, 'gone.md', cx, cy);

    await expect
      .poll(async () => (await deadLinks(window, f.gridID)).length, { timeout: 15_000 })
      .toBe(1);
    const row = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;
    expect(row.reference, 'still a link').toBe(true);
    expect(row.altText, 'the label survives').toBe('gone.md');
    await window.waitForTimeout(1_500);
    const e = await errors(window);
    expect(e.notices, 'a gone target raises no notice').toEqual([]);
    expect(e.stripH, 'and reserves no strip height').toBe(0);

    // The file comes back, and a write into its grid is the namespace saying
    // its listing changed.
    fs.writeFileSync(file, '# gone\nback\n');
    await placeTile(gw.origin, entry!.id, 0, collection, 9, 9, 1, 1);
    await expect
      .poll(async () => (await deadLinks(window, f.gridID)).length, { timeout: 15_000 })
      .toBe(0);
    expect((await errors(window)).notices, 'and coming back is quiet too').toEqual([]);

    await gw.deleteTileCell(cx, cy);
    await expect
      .poll(async () => tileAt(await gw.getGrid(f.gridID), 'text', cx, cy))
      .toBeUndefined();
  });
});

// A break deeper in the chain is the far node's to judge: the link names a
// declared connection, so the client asks, and the far node answers the dead
// verdict for the hop it does not declare. That answer draws the tile dead
// exactly as an undeclared first hop does, says nothing, is asked no more, and
// leaves the link deletable.
test.describe('a link broken beyond a declared connection', () => {
  test.use({ extraNodes: ['second'] });

  test('renders dead, quietly, once the far node says so', async ({ gw, window }) => {
    const second = (await gw.plugins()).find((p) => p.label === 'second')!;
    expect(second?.rootGridID, 'the connection lands').toBeTruthy();
    // "<node>/second/<far>/<root>" → "<node>/second/<far>/nope/1": the far node
    // declares no connection "nope".
    const deep = second.rootGridID.replace(/\/[^/]+$/, '/nope/1');

    await gw.enterPlugin('home');
    const f = await gw.focused();
    const cx = Math.round(f.cx) + 1;
    const cy = Math.round(f.cy) + 1;

    const asked: string[] = [];
    await window.route('**/gridwell.v1.Gridwell/GetGrid', async (r: any) => {
      asked.push(r.request().postData() ?? '');
      await r.continue();
    });

    await createExitWell(gw.origin, f.gridID, deep, 'far room', cx, cy);
    await expect
      .poll(async () => (await deadLinks(window, f.gridID)).length, { timeout: 15_000 })
      .toBe(1);

    await window.waitForTimeout(1_500);
    const e = await errors(window);
    expect(e.notices, 'a dead link raises no notice, however deep the break').toEqual([]);
    expect(
      asked.filter((body) => body.includes('/nope/1')).length,
      'asked once, and never again once the verdict stands',
    ).toBe(1);

    const before = await gw.focused();
    await gw.descendCell(cx, cy);
    expect((await gw.focused()).placeDepth, 'a dead link is not a doorway').toBe(before.placeDepth);

    await window.unroute('**/gridwell.v1.Gridwell/GetGrid');
    await gw.deleteTileCell(cx, cy);
    await expect
      .poll(async () => tileAt(await gw.getGrid(f.gridID), 'well', cx, cy))
      .toBeUndefined();
    expect((await errors(window)).notices, 'and the delete is quiet too').toEqual([]);
  });
});
