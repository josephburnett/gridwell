import * as fs from 'node:fs';
import * as path from 'node:path';
import { test, expect } from './fixtures';
import { placeTile, tileAt } from './oracle';
import { makeRunDir } from './homes';

// A page a plugin serves has no address of its own, so a page row its source
// is not listing right now must still be that page: a click never asks the
// user for an address the plugin would refuse. A source that cannot be read is
// health, so the row keeps presenting as the page it was.

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
