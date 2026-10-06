import { test, expect } from './fixtures';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { makeRunDir } from './homes';

// A page its plugin serves changes at its source: the live view showing it
// loads it again, and once it is left, the screenshot of the old page gives
// way when the page changes again (urlview.PageMoved, pluginhost.pageFaceStale).

const siteDir = makeRunDir();
const pagePath = path.join(siteDir, 'note.html');
fs.writeFileSync(pagePath, '<title>note</title><body>first</body>');

test.use({ extraPlugins: [{ kind: 'fs', name: 'site', config: { root: siteDir } }] });

test('a served page that changes reloads in its live view, and its screenshot gives way', async ({
  electronApp,
  gw,
}) => {
  await gw.enterPlugin('site');
  const f = await gw.focused();
  const note = (await gw.getGrid(f.gridID)).tiles!.find((t) => t.altText === 'note.html')!;
  expect(note.servesPage, 'an html file is a page its plugin serves').toBe(true);

  const pageText = () =>
    electronApp.evaluate(async ({ webContents }) => {
      const wc = webContents.getAllWebContents().find((w) => w.getURL().includes('/content/'));
      return wc ? String(await wc.executeJavaScript('document.body ? document.body.textContent : ""')) : '';
    });

  await gw.descendCell(Number(note.x ?? 0), Number(note.y ?? 0));
  await expect.poll(pageText, { message: 'the descent opens the page live', timeout: 15_000 }).toBe('first');

  fs.writeFileSync(pagePath, '<title>note</title><body>second</body>');
  await expect
    .poll(pageText, { message: 'the live view shows the page its source changed, with no gesture', timeout: 15_000 })
    .toBe('second');

  await gw.ascendViaCrumb();
  const faceKey = async () =>
    Number((await gw.getGrid(f.gridID)).tiles!.find((t) => t.altText === 'note.html')?.previewBlobId ?? 0);
  await expect.poll(faceKey, { message: 'the ascent freezes the page into its face', timeout: 15_000 }).toBeGreaterThan(0);
  const shot = await faceKey();

  fs.writeFileSync(pagePath, '<title>note</title><body>third</body>');
  await expect
    .poll(faceKey, { message: 'the screenshot of the page before the change is no longer the face', timeout: 15_000 })
    .not.toBe(shot);
  expect(await faceKey(), 'the face is the plugin\'s until the next capture').toBeLessThanOrEqual(0);
});
