import { test, expect } from './fixtures';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { makeRunDir } from './homes';

// A link out of a page the /content/ door serves behaves like a link out of
// any live page: target=_blank opens an ephemeral visit in a pane split below
// and the served page stays put. The door's sandbox must allow popups for
// that, or the click never reaches the window-open handler. A plain link
// navigates in place, which is the web's own behaviour.

const siteDir = makeRunDir();

test.use({ extraPlugins: [{ kind: 'fs', name: 'site', config: { root: siteDir } }] });

test('a target=_blank link out of a served page opens below', async ({ electronApp, gw }) => {
  const blank = `${gw.origin}/wasm_exec.js?served=blank`;
  const plain = `${gw.origin}/wasm_exec.js?served=plain`;
  fs.writeFileSync(
    path.join(siteDir, 'links.html'),
    `<!doctype html><a id="blank" target="_blank" href="${blank}">blank</a> ` +
      `<a id="plain" href="${plain}">plain</a>`,
  );

  await gw.enterPlugin('site');
  const f = await gw.focused();
  const page = (await gw.getGrid(f.gridID)).tiles!.find((t) => t.altText === 'links.html')!;
  expect(page, 'the fs root lists links.html').toBeTruthy();
  expect(page.servesPage, 'an html file is a served page').toBe(true);
  await gw.descendCell(Number(page.x ?? 0), Number(page.y ?? 0));

  const served = new RegExp(`/content/[0-9a-f]{64}/${page.id}/$`);
  const servedURL = () =>
    electronApp.evaluate(({ webContents }) =>
      webContents
        .getAllWebContents()
        .map((w) => w.getURL())
        .find((u) => u.includes('/content/')),
    );
  await expect.poll(servedURL, { timeout: 15_000 }).toMatch(served);
  await gw.waitIdle();
  const panesBefore = (await gw.panes()).length;

  const click = (id: string) =>
    electronApp.evaluate(async ({ webContents }, id: string) => {
      const wc = webContents.getAllWebContents().find((w) => w.getURL().includes('/content/'));
      if (!wc) throw new Error('served view not found');
      await wc.executeJavaScript(`document.getElementById(${JSON.stringify(id)}).click()`, true);
    }, id);

  await click('blank');
  await expect
    .poll(async () => (await gw.panes()).length, {
      message: 'the target=_blank link split the pane',
      timeout: 15_000,
    })
    .toBe(panesBefore + 1);
  const panes = (await gw.panes()).slice().sort((a, b) => a.y - b.y);
  const lower = panes[panes.length - 1];
  expect(lower.focused, 'the new lower pane took focus').toBe(true);
  expect(lower.y, 'the new pane sits below').toBeGreaterThan(panes[0].y);
  await expect
    .poll(
      () =>
        electronApp.evaluate(({ webContents }, u: string) =>
          webContents.getAllWebContents().some((w) => w.getURL() === u),
        blank),
      { message: 'the link opened live below at its address', timeout: 15_000 },
    )
    .toBe(true);
  expect(await servedURL(), 'the served page stayed where it was').toMatch(served);

  // A plain link navigates the served view itself and splits nothing.
  await click('plain');
  await expect
    .poll(
      () =>
        electronApp.evaluate(({ webContents }, u: string) =>
          webContents.getAllWebContents().some((w) => w.getURL() === u),
        plain),
      { message: 'a plain link navigates in place', timeout: 15_000 },
    )
    .toBe(true);
  expect((await gw.panes()).length, 'a plain link opens no pane').toBe(panesBefore + 1);
});
