import { test as base, expect } from '@playwright/test';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { spawnServe, stopServe, authenticate, freePort } from './fixtures';
import { seedHome } from '../e2e/fixtures';
import { makeRunDir, removeHome } from '../e2e/homes';
import { GridwellDriver } from '../e2e/driver';
import { tileAt } from '../e2e/oracle';

// A dangling doorway, a link into a plugin no longer in server.yaml, is DEAD:
// the node does not declare that namespace, so the tile greys, nothing is
// asked for it, and nothing is said about it (client/deadref).
//
// dead-link.spec.ts pins the state against a reference seeded straight onto
// the server. This crosses the other half of the seam, in browser mode: a link
// the user really dropped on a plugin really in server.yaml, then the config
// edit and the reboot that make it dangle. It counts verdicts and fetches over
// three seconds of rendering, both of which must be zero.

base('a link into an unconfigured plugin goes dead, quietly', async ({ page }) => {
  const docs = makeRunDir();
  fs.writeFileSync(path.join(docs, 'note.md'), 'hello');
  const home = seedHome([{ kind: 'fs', name: 'files', config: { root: docs } }]);

  // Boot 1: with the plugin. Drop a link to it on the home root.
  let linkID = '';
  let childGridID = '';
  const served1 = await spawnServe(home, await freePort());
  try {
    await authenticate(page, served1);
    await page.goto(served1.origin + '/?e2e=1');
    await page.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
    const gw = new GridwellDriver(page, served1.origin);
    await gw.waitIdle();
    const f = await gw.focused();
    await gw.openPalette();
    await gw.dragPluginLink('files', Math.round(f.cx), Math.round(f.cy));
    await gw.waitIdle();
    const seeded: any = await gw.getGrid(f.gridID);
    const link = (seeded.tiles ?? []).find((t: any) => t.kind === 'well');
    expect(link, 'boot 1 dropped the plugin link well').toBeTruthy();
    expect(link.reference, 'and it is a link, not a well of its own').toBe(true);
    linkID = link.id;
    childGridID = String(link.childGridId ?? '');
    expect(childGridID, 'the link names the plugin root it points at').toBeTruthy();
  } finally {
    await stopServe(served1.child);
  }

  // The conversion-shaped edit: the plugin leaves the config, the home and its
  // link tile stay. Serve minted ids into server.yaml on boot 1, so keep
  // everything but the plugins list.
  const yaml = fs.readFileSync(path.join(home, 'server.yaml'), 'utf8');
  const idLine = yaml.split('\n').find((l) => l.startsWith('id:'));
  expect(idLine, 'boot 1 minted the node id').toBeTruthy();
  fs.writeFileSync(path.join(home, 'server.yaml'), idLine + '\n');

  // Boot 2: the well is a dangling doorway. Count everything it could say: the
  // strip's verdict on the console, and every grid the client asks for.
  const unavailable: string[] = [];
  page.on('console', (m) => {
    if (m.text().includes('grid unavailable')) unavailable.push(m.text());
  });
  const asked: string[] = [];
  await page.route('**/gridwell.v1.Gridwell/GetGrid', async (r) => {
    asked.push(r.request().postData() ?? '');
    await r.continue();
  });
  const served2 = await spawnServe(home, await freePort());
  try {
    await authenticate(page, served2);
    await page.goto(served2.origin + '/?e2e=1');
    await page.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
    const gw = new GridwellDriver(page, served2.origin);
    await gw.waitIdle();
    const f = await gw.focused();

    // server.yaml lost the plugin, so the tile the roster cannot place reads
    // dead.
    await expect
      .poll(
        async () =>
          (await page.evaluate(
            (gid: string) => (window as any).__gridwellTest.deadLinks(gid),
            f.gridID,
          )) as string[],
        { message: 'the dangling doorway reads dead', timeout: 15_000 },
      )
      .toEqual([linkID]);

    // Let the render loop run with the dangling well on screen. Nothing is
    // asked and nothing is said, not once per frame and not once at all.
    await page.waitForTimeout(3_000);
    expect(unavailable, 'a dead link raises no verdict').toEqual([]);
    expect(
      asked.filter((body) => body.includes(childGridID)),
      'a dead namespace is never asked for',
    ).toEqual([]);

    // Still a link, still labelled, and still deletable.
    const row = (await gw.getGrid(f.gridID)).tiles!.find((t: any) => t.id === linkID)!;
    expect(row.reference, 'still a link: deleting it only unlinks').toBe(true);
    await page.unroute('**/gridwell.v1.Gridwell/GetGrid');
    await gw.deleteTileCell(Number(row.x ?? 0), Number(row.y ?? 0));
    await expect
      .poll(async () => tileAt(await gw.getGrid(f.gridID), 'well', Number(row.x ?? 0), Number(row.y ?? 0)))
      .toBeUndefined();
    expect(unavailable, 'and the delete is quiet too').toEqual([]);
  } finally {
    await stopServe(served2.child);
    removeHome(home);
    fs.rmSync(docs, { recursive: true, force: true });
  }
});
