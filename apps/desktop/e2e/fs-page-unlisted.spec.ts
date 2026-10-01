import * as fs from 'node:fs';
import * as path from 'node:path';
import { test, expect } from './fixtures';
import { createLeafLink, placeTile, tileAt } from './oracle';
import { makeRunDir } from './homes';

// A page a plugin serves has no address of its own, so a page row its source
// is not listing right now must still be that page: a click never asks the
// user for an address the plugin would refuse. A source that cannot be read is
// health, so the row keeps presenting as the page it was; a file that is gone
// is a gone key, so a link to it is dead, and a dead link asks nothing either.

// One 1x1 png: an image is a page the fs plugin serves.
const PNG = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==',
  'base64',
);

const modal = (window: any) => window.locator('#gw-url-modal.open');

test.describe('a page whose source goes dark', () => {
  const siteDir = makeRunDir();
  fs.writeFileSync(path.join(siteDir, 'kept.png'), PNG);
  test.use({ extraPlugins: [{ kind: 'fs', name: 'site', config: { root: siteDir } }] });

  test('still presents as the page, and a click descends into it', async ({ gw, window }) => {
    await gw.enterPlugin('site');
    const f = await gw.focused();
    const page = (await gw.getGrid(f.gridID)).tiles!.find((t) => t.altText === 'kept.png')!;
    expect(page?.servesPage, 'an image is a page the plugin serves').toBe(true);
    const cx = Math.round(f.cx);
    const cy = Math.round(f.cy);
    // A row is what answers while the source cannot: the move mints it.
    await placeTile(gw.origin, page.id!, page.version as number | undefined, f.gridID, cx, cy + 1, 1, 1);

    fs.chmodSync(siteDir, 0o000);
    try {
      // A second move is a write into the grid, which every open view re-reads,
      // now against a source that cannot list.
      await placeTile(gw.origin, page.id!, page.version as number | undefined, f.gridID, cx + 1, cy + 1, 1, 1);
      await expect
        .poll(async () => (await gw.getGrid(f.gridID)).tiles!.map((t) => t.altText), { timeout: 15_000 })
        .toEqual(['kept.png']);
      const dark = tileAt(await gw.getGrid(f.gridID), 'url', cx + 1, cy + 1)!;
      expect(dark?.servesPage, 'the dark row is still the page').toBe(true);
      expect(dark.urlString ?? '', "and the address is still the plugin's").toBe('');

      await gw.descendCell(cx + 1, cy + 1);
      await expect
        .poll(async () => (await gw.focused()).textFocus, { message: 'the click descends into the page', timeout: 15_000 })
        .toBe(page.id);
      await expect(modal(window), 'and asks for no address').toHaveCount(0);
    } finally {
      fs.chmodSync(siteDir, 0o755);
    }
  });
});

test.describe('a page whose file is gone', () => {
  const siteDir = makeRunDir();
  test.use({ extraPlugins: [{ kind: 'fs', name: 'site', config: { root: siteDir } }] });

  test('a link to it greys, and a click asks nothing', async ({ gw, window }) => {
    const file = path.join(siteDir, 'gone.png');
    fs.writeFileSync(file, PNG);
    const site = (await gw.plugins()).find((p) => p.label === 'site')!;
    const collection = site.menuEntries[0]!.gridID;
    const entry = ((await gw.getGrid(collection)).tiles ?? []).find((t) => t.altText === 'gone.png');
    expect(entry?.servesPage, 'the image is listed as a page').toBe(true);
    fs.rmSync(file);

    await gw.enterPlugin('home');
    const f = await gw.focused();
    const cx = Math.round(f.cx) + 1;
    const cy = Math.round(f.cy) + 1;
    await createLeafLink(gw.origin, f.gridID, entry!.id!, 'gone.png', cx, cy, 'url');
    await expect.poll(async () => tileAt(await gw.getGrid(f.gridID), 'url', cx, cy)?.reference).toBe(true);

    // A url link shows no face of its own, so the click is the first read of
    // its target, and that read is what hears the target is gone.
    const before = await gw.focused();
    await gw.descendCell(cx, cy);
    await expect
      .poll(
        () => window.evaluate((gid: string) => (window as any).__gridwellTest.deadLinks(gid), f.gridID),
        { message: 'the link to a gone page is dead', timeout: 15_000 },
      )
      .toHaveLength(1);
    await expect(modal(window), 'a dead page asks for no address').toHaveCount(0);
    await expect
      .poll(async () => (await gw.focused()).placeDepth, { message: 'the pane leaves the dead link', timeout: 15_000 })
      .toBe(before.placeDepth);
    expect((await gw.focused()).textFocus, 'and opens nothing').toBe('');
    const errs = await window.evaluate(() => (window as any).__gridwellTest.errors());
    expect(errs.notices, 'a dead link says nothing').toEqual([]);
    expect(tileAt(await gw.getGrid(f.gridID), 'url', cx, cy)?.altText, 'still labelled').toBe('gone.png');

    // Now that it is known dead, a click is no doorway at all.
    await gw.descendCell(cx, cy);
    expect((await gw.focused()).placeDepth, 'a dead link is not a doorway').toBe(before.placeDepth);
    await expect(modal(window)).toHaveCount(0);
  });
});
