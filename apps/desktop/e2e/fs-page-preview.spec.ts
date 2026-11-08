import { test, expect } from './fixtures';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { makeRunDir } from './homes';

// A page the node serves wears the preview an internet page does. Before the
// first visit a PDF has no picture, since only an image is its own, so
// nothing asks for one; the ascent out of the live page freezes it, and from
// then on the preview is that screenshot, served by the node.

// A one-page PDF with no text, as small as a reader still opens.
const PDF = [
  '%PDF-1.4',
  '1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj',
  '2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj',
  '3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 200 200]>>endobj',
  'trailer<</Root 1 0 R>>',
  '%%EOF',
].join('\n');

const siteDir = makeRunDir();
fs.writeFileSync(path.join(siteDir, 'page.pdf'), PDF);

test.use({ filesRoots: [{ root: siteDir, label: 'site' }] });

test('a served page with no picture is not asked for one, and its screenshot becomes its preview', async ({
  electronApp,
  gw,
  window,
}) => {
  // Every GetTilePreview the client makes for the page, with the bytes each
  // one answered.
  const asked: number[] = [];
  let pageID = '';
  await window.route('**/gridwell.v1.Gridwell/GetTilePreview', async (r: any) => {
    if (!pageID || !(r.request().postData() ?? '').includes(pageID)) return r.continue();
    const resp = await r.fetch();
    const body = await resp.json().catch(() => ({}));
    asked.push(Buffer.from(String(body.jpeg ?? ''), 'base64').length);
    return r.fulfill({ response: resp });
  });

  await gw.enterSource('site (files)');
  const f = await gw.focused();
  const page = (await gw.getGrid(f.gridID)).tiles!.find((t) => t.altText === 'page.pdf')!;
  expect(page, 'the root lists page.pdf').toBeTruthy();
  expect(page.servesPage, 'a PDF is a page the node serves').toBe(true);
  expect(Number(page.previewBlobId ?? 0), 'an unvisited PDF has no preview').toBe(0);
  pageID = page.id!;

  // A second of the tile on screen is dozens of frames, each one a chance to ask.
  await gw.waitIdle();
  await window.waitForTimeout(1_000);
  expect(asked.length, 'GetTilePreview calls for a page with no preview').toBe(0);

  const live = () =>
    electronApp.evaluate(({ webContents }) =>
      webContents.getAllWebContents().some((w) => w.getURL().includes('/content/')),
    );
  await gw.descendCell(Number(page.x ?? 0), Number(page.y ?? 0));
  await expect.poll(live, { message: 'the descent opens the page live', timeout: 15_000 }).toBe(true);
  // Let the page paint, so the freeze has a paint to take.
  await window.waitForTimeout(500);
  await gw.ascendViaCrumb();
  await expect.poll(live, { timeout: 15_000 }).toBe(false);

  await expect
    .poll(
      async () =>
        Number((await gw.getGrid(f.gridID)).tiles!.find((t) => t.altText === 'page.pdf')?.previewBlobId ?? 0),
      { message: 'the ascent stores the screenshot as the preview', timeout: 15_000 },
    )
    .toBeGreaterThan(0);
  const after = (await gw.getGrid(f.gridID)).tiles!.find((t) => t.altText === 'page.pdf')!;
  expect(after.urlString ?? '', 'it still carries no address').toBe('');
  expect(Number(after.version ?? 0), 'a freeze claims no content bytes').toBe(Number(page.version ?? 0));

  // A reload empties the preview cache, so the grid's draw asks for the preview,
  // and the node answers with the screenshot.
  await window.reload();
  await window.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
  await expect.poll(async () => (await gw.focused()).gridID, { timeout: 30_000 }).toBe(f.gridID);
  await expect
    .poll(() => asked.filter((n) => n > 0).length, {
      message: 'the preview is fetched and carries the screenshot',
      timeout: 15_000,
    })
    .toBeGreaterThan(0);
  await window.unroute('**/gridwell.v1.Gridwell/GetTilePreview');
});
