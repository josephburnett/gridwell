import { test, expect } from './fixtures';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { makeRunDir } from './homes';

// A page a plugin serves wears the face an internet page does. Before the
// first visit an html page has no picture, so nothing asks for one; the ascent
// out of the live page freezes it, and from then on the face is that
// screenshot, served by the node.

const siteDir = makeRunDir();
fs.writeFileSync(
  path.join(siteDir, 'page.html'),
  '<html><body style="background:#2a6"><h1>a served page</h1></body></html>',
);

test.use({ extraPlugins: [{ kind: 'fs', name: 'site', config: { root: siteDir } }] });

test('a served page with no picture is not asked for one, and its screenshot becomes its face', async ({
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

  await gw.enterPlugin('site');
  const f = await gw.focused();
  const page = (await gw.getGrid(f.gridID)).tiles!.find((t) => t.altText === 'page.html')!;
  expect(page, 'the fs root lists page.html').toBeTruthy();
  expect(page.servesPage, 'an html file is a page the plugin serves').toBe(true);
  expect(Number(page.previewBlobId ?? 0), 'an unvisited html page has no face').toBe(0);
  pageID = page.id!;

  // A second of the tile on screen is dozens of frames, each one a chance to ask.
  await gw.waitIdle();
  await window.waitForTimeout(1_000);
  expect(asked.length, 'GetTilePreview calls for a page with no face').toBe(0);

  const live = () =>
    electronApp.evaluate(({ webContents }) =>
      webContents.getAllWebContents().some((w) => w.getURL().includes('/content/')),
    );
  await gw.descendCell(Number(page.x ?? 0), Number(page.y ?? 0));
  await expect.poll(live, { message: 'the descent opens the page live', timeout: 15_000 }).toBe(true);
  // Let the page paint, so the freeze has a frame to take.
  await window.waitForTimeout(500);
  await gw.ascendViaCrumb();
  await expect.poll(live, { timeout: 15_000 }).toBe(false);

  await expect
    .poll(
      async () =>
        Number((await gw.getGrid(f.gridID)).tiles!.find((t) => t.altText === 'page.html')?.previewBlobId ?? 0),
      { message: 'the ascent stores the screenshot as the face', timeout: 15_000 },
    )
    .toBeGreaterThan(0);
  const after = (await gw.getGrid(f.gridID)).tiles!.find((t) => t.altText === 'page.html')!;
  expect(after.urlString ?? '', "the address stays the plugin's").toBe('');
  expect(Number(after.version ?? 0), 'a freeze claims no content bytes').toBe(Number(page.version ?? 0));

  // A reload empties the preview cache, so the grid's draw asks for the face,
  // and the node answers with the screenshot.
  await window.reload();
  await window.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
  await expect.poll(async () => (await gw.focused()).gridID, { timeout: 30_000 }).toBe(f.gridID);
  await expect
    .poll(() => asked.filter((n) => n > 0).length, {
      message: 'the face is fetched and carries the screenshot',
      timeout: 15_000,
    })
    .toBeGreaterThan(0);
  await window.unroute('**/gridwell.v1.Gridwell/GetTilePreview');
});
