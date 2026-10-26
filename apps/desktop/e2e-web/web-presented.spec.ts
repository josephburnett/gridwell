import * as http from 'node:http';
import type { AddressInfo } from 'node:net';
import { test, expect } from './fixtures';
import { tileAt } from '../e2e/oracle';
import { PAGES, presentedYaml } from '../e2e/presented';

// A presented url tile on a browser host: with no bridge it cannot go live in
// place, so the bar circle opens its page in a new tab, and the page is the
// plugin's for the site address, not the site. An outside link opens the site
// in a new tab, which on this host is the default browser, and makes nothing.
// A link inside the plugin's page to an address it does not take leaves for
// the site: nothing can ask first in a tab, so the node redirects.

test.use({ extraYaml: presentedYaml });

test('a presented url tile opens its plugin page in a new tab, and an outside link opens the site', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

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
  await gw.waitIdle();

  const menu = await gw.menu();
  const [popup] = await Promise.all([
    window.context().waitForEvent('page', { timeout: 10_000 }),
    window.mouse.click(menu.plusX, menu.plusY),
  ]);
  await popup.waitForURL(/\/content\//, { timeout: 10_000 });
  expect(popup.url()).toMatch(new RegExp(`/content/[0-9a-f]{64}/${tile.id}/example\\.test/hello$`));
  await expect.poll(() => popup.title(), { message: "the tab shows the plugin's page", timeout: 10_000 }).toBe('Hello, world');
  const site = http.createServer((_req, res) => {
    res.setHeader('Content-Type', 'text/html');
    res.end('<title>Plain site</title>');
  });
  await new Promise<void>((r) => site.listen(0, '127.0.0.1', () => r()));
  try {
    const host = `127.0.0.1:${(site.address() as AddressInfo).port}`;
    await popup.evaluate((h) => {
      const a = document.createElement('a');
      a.href = location.href.replace(/example\.test\/hello$/, h + '/plain');
      document.body.append(a);
      a.click();
    }, host);
    await popup.waitForURL(`http://${host}/plain`, { timeout: 10_000 });
    await expect.poll(() => popup.title(), { message: 'the tab left for the site', timeout: 10_000 }).toBe('Plain site');
  } finally {
    site.close();
  }
  await popup.close();
  await gw.middleClickCell(cx, cy);

  // A rendered text tile's link to an address an outside route takes.
  await gw.openMenu();
  await gw.dragCreate('markdown', cx + 3, cy);
  await gw.descendCell(cx + 3, cy);
  await gw.typeText('[out](http://outside.test/x)');
  await gw.toggleTextMode();
  await expect.poll(async () => (await gw.focused()).textMode).toBe('rendered');
  await window.evaluate(() => {
    const w = window as any;
    w.__gwOpened = [];
    w.open = (...args: unknown[]) => {
      w.__gwOpened.push(args);
      return null;
    };
  });
  const panes = (await gw.panes()).length;
  const scratch = ((await gw.getGrid(await gw.scratchGridID('home'))).tiles ?? []).length;
  await window.locator('#gw-rendered-view a').click();
  await expect
    .poll(() => window.evaluate(() => (window as any).__gwOpened), { timeout: 10_000 })
    .toEqual([['http://outside.test/x', '_blank', 'noopener']]);
  await gw.waitIdle();
  expect((await gw.panes()).length, 'an outside link opens no pane').toBe(panes);
  expect(((await gw.getGrid(await gw.scratchGridID('home'))).tiles ?? []).length, 'and makes no tile').toBe(scratch);
});
