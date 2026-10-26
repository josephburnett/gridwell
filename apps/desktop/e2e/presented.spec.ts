import { test, expect, homePassword, loginToken } from './fixtures';
import { tileAt } from './oracle';
import { dumpViaAPI } from './trace';
import { PAGES, presentedYaml } from './presented';

// A plugin may present a site (CLAUDE.md, 2026-10-25): a url tile whose
// address a route sends to a plugin that presents it opens that plugin's page
// for the address, stays a url tile whose content is the site address, and
// writes back where an in-page navigation lands. A link to an address an
// outside route takes opens in the default browser and creates nothing.

test.use({ extraYaml: presentedYaml });

test('a presented url tile opens its plugin page, writes back the site address, and an outside link opens outside', async ({
  electronApp,
  gw,
  home,
  window,
}) => {
  const c = await gw.cadences();
  const token = await loginToken(gw.origin, homePassword(home));
  await gw.enterPlugin('home');
  const scratchGridID = await gw.scratchGridID('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  const top = f.id;

  const inPane = (paneId: string, js: string) =>
    electronApp.evaluate(
      (_e, a: { id: string; js: string }) =>
        (globalThis as any).__gwRegistry.webContentsFor(a.id)?.executeJavaScript(a.js, true) ?? null,
      { id: paneId, js },
    );
  const urlIn = (paneId: string) =>
    electronApp.evaluate(
      (_e, id: string) => ((globalThis as any).__gwRegistry.webContentsFor(id)?.getURL() as string) ?? '',
      paneId,
    );
  const addLink = (paneId: string, id: string, href: string) =>
    inPane(
      paneId,
      `document.body.insertAdjacentHTML('beforeend', '<a id="${id}" target="_blank" href="${href}">${id}</a>');` +
        `document.getElementById('${id}').click()`,
    );

  // (1) The tile's address is the site's; its live view is the plugin's page.
  await gw.openMenu();
  await gw.dragCreate('url', cx, cy);
  await gw.descendCell(cx, cy);
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', 'http://example.test/hello');
  await window.locator('#gw-url-form').evaluate((fm: HTMLFormElement) => fm.requestSubmit());
  const row = async () => tileAt(await gw.getGrid(f.gridID), 'url', cx, cy);
  await expect.poll(async () => (await row())?.urlString ?? '', { timeout: 10_000 }).toBe('http://example.test/hello');
  const tile = (await row())!;
  expect(tile.presentedBy, 'the node stamps the plugin the route sends it to').toContain(PAGES);
  await expect
    .poll(() => urlIn(top), { message: 'the view opens at the plugin page for the address', timeout: 15_000 })
    .toMatch(new RegExp(`/content/[0-9a-f]{64}/${tile.id}/example\\.test/hello$`));
  await expect.poll(() => inPane(top, 'document.title'), { timeout: 15_000 }).toBe('Hello, world');
  expect(await inPane(top, `document.querySelector('footer').textContent`)).toContain('the key hello');
  await gw.waitIdle();
  const panesBefore = (await gw.panes()).length;

  // (2) A link to another presented address opens below, at the plugin's page
  // for it, its assets served from the plugin's _/.
  await addLink(top, 'gwstyled', 'http://example.test/styled');
  await expect
    .poll(async () => (await gw.panes()).length, { message: 'the link split the pane', timeout: 15_000 })
    .toBe(panesBefore + 1);
  const panes = (await gw.panes()).slice().sort((a, b) => a.y - b.y);
  const lower = panes[panes.length - 1];
  expect(lower.focused, 'the pane below took focus').toBe(true);
  await expect
    .poll(() => urlIn(lower.id), { timeout: 15_000 })
    .toMatch(/\/content\/[0-9a-f]{64}\/.+\/example\.test\/styled$/);
  await expect.poll(() => inPane(lower.id, 'document.title'), { timeout: 15_000 }).toBe('A page with resources');
  await expect
    .poll(() => inPane(lower.id, `getComputedStyle(document.querySelector('.card')).borderTopWidth`), {
      message: 'the stylesheet under _/ reached the page',
      timeout: 15_000,
    })
    .toBe('2px');
  expect(await urlIn(top), 'the page it came from stayed').toMatch(/example\.test\/hello$/);

  // (3) An in-page navigation is written back as the site address.
  const before = Number(tile.version ?? 0);
  await inPane(top, `document.querySelector('nav a[href$="/report"]').click()`);
  await expect.poll(() => urlIn(top), { timeout: 15_000 }).toMatch(/example\.test\/report$/);
  await expect
    .poll(async () => (await row())?.urlString ?? '', {
      message: 'the tile follows where the user went, as the site address',
      timeout: c.urlAddressMs * 20,
    })
    .toBe('http://example.test/report');
  expect(Number((await row())!.version ?? 0)).toBeGreaterThan(before);

  // (4) An outside link opens in the default browser and creates nothing.
  // shell.openExternal is replaced here, in the main process, so this box's
  // browser is never launched; the verb under test is main's own.
  await electronApp.evaluate(({ shell }) => {
    const g = globalThis as any;
    g.__gwOutside = [];
    (shell as any).openExternal = async (u: string) => {
      g.__gwOutside.push(u);
    };
  });
  const scratchBefore = ((await gw.getGrid(scratchGridID)).tiles ?? []).length;
  const panesNow = (await gw.panes()).length;
  await addLink(top, 'gwout', 'http://outside.test/x');
  await expect
    .poll(() => electronApp.evaluate(() => (globalThis as any).__gwOutside as string[]), { timeout: 10_000 })
    .toEqual(['http://outside.test/x']);
  const outside = async () =>
    (await dumpViaAPI(gw.origin, token)).filter(
      (l) => l.origin === 'electron' && l.src === 'webview' && l.kind === 'outside',
    );
  await expect.poll(async () => (await outside()).map((l) => [l.msg, l.kv?.verdict]), { timeout: 10_000 }).toEqual([
    ['http://outside.test/x', 'opened'],
  ]);
  await gw.waitIdle();
  expect((await gw.panes()).length, 'an outside link opens no pane').toBe(panesNow);
  expect(((await gw.getGrid(scratchGridID)).tiles ?? []).length, 'and makes no tile').toBe(scratchBefore);
  expect(
    await electronApp.evaluate(({ webContents }) =>
      webContents.getAllWebContents().some((w) => w.getURL().includes('outside.test')),
    ),
    'nothing in the app loaded the outside address',
  ).toBe(false);

  // pages declares no Watch, which is no failure, so no notice.
  const e = await window.evaluate(() => (window as any).__gridwellTest.errors());
  expect(e.notices, 'no notices').toHaveLength(0);
});
