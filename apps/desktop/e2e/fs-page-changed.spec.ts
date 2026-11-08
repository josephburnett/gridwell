import { test, expect } from './fixtures';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { makeRunDir } from './homes';

// A page the node serves changes on disk: the live view showing it loads it
// again, and once it is left, the screenshot of the old page gives way when
// the page changes again (urlview.PageMoved, files.pagePreviewStale).

// Three PNGs one pixel high, one, two and three pixels wide, so the live
// view's image says which bytes it loaded.
const png = (b64: string) => Buffer.from(b64, 'base64');
const WIDE = [
  png('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=='),
  png('iVBORw0KGgoAAAANSUhEUgAAAAIAAAABCAIAAAB7QOjdAAAADUlEQVR4nGNQWJAARAAHZwJBKpaxggAAAABJRU5ErkJggg=='),
  png('iVBORw0KGgoAAAANSUhEUgAAAAMAAAABCAIAAACUgoPjAAAADUlEQVR4nGNQWJAAQQAQKgNh7aJADQAAAABJRU5ErkJggg=='),
];

const siteDir = makeRunDir();
const pagePath = path.join(siteDir, 'note.png');
fs.writeFileSync(pagePath, WIDE[0]);

test.use({ filesRoots: [{ root: siteDir, label: 'site' }] });

test('a served page that changes reloads in its live view, and its screenshot gives way', async ({
  electronApp,
  gw,
}) => {
  await gw.enterSource('site (files)');
  const f = await gw.focused();
  const note = (await gw.getGrid(f.gridID)).tiles!.find((t) => t.altText === 'note.png')!;
  expect(note.servesPage, 'an image is a page the node serves').toBe(true);

  const pageWidth = () =>
    electronApp.evaluate(async ({ webContents }) => {
      const wc = webContents.getAllWebContents().find((w) => w.getURL().includes('/content/'));
      return wc ? Number(await wc.executeJavaScript('document.images[0] ? document.images[0].naturalWidth : 0')) : 0;
    });

  await gw.descendCell(Number(note.x ?? 0), Number(note.y ?? 0));
  await expect.poll(pageWidth, { message: 'the descent opens the page live', timeout: 15_000 }).toBe(1);

  fs.writeFileSync(pagePath, WIDE[1]);
  await expect
    .poll(pageWidth, { message: 'the live view shows the page the disk changed, with no gesture', timeout: 15_000 })
    .toBe(2);

  await gw.ascendViaCrumb();
  const previewKey = async () =>
    Number((await gw.getGrid(f.gridID)).tiles!.find((t) => t.altText === 'note.png')?.previewBlobId ?? 0);
  await expect.poll(previewKey, { message: 'the ascent freezes the page into its preview', timeout: 15_000 }).toBeGreaterThan(0);
  const shot = await previewKey();

  fs.writeFileSync(pagePath, WIDE[2]);
  await expect
    .poll(previewKey, { message: 'the screenshot of the page before the change is no longer the preview', timeout: 15_000 })
    .not.toBe(shot);
  expect(await previewKey(), "the preview is the image's own until the next capture").toBeLessThanOrEqual(0);
});
