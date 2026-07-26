import { test, expect } from './fixtures';

// Issue #136: "Clear Site Data (<host>)" on the live view's context menu
// wipes the CURRENT SITE from the plugin's partition — cookies by
// domain-suffix match plus the origin's storage — and reloads. Other sites'
// cookies in the same partition survive (the scope is site × plugin, not a
// partition nuke). Playwright cannot click a native menu, so the spec drives
// the same registry action the menu item calls.

test('clear site data wipes this site and spares others', async ({
  electronApp,
  window,
  gw,
}) => {
  await gw.enterPlugin('localdb');
  await gw.clickPaletteSwatch('url');
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?site=1`);
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();
  // Poll for the NAVIGATED view, not a webContents count: the count grows at
  // view creation, before loadURL lands, and the old count-poll only stayed
  // ahead of that race by riding the session-hydrate await that place() no
  // longer performs (one host-local session, 2026-07-26).
  await expect
    .poll(
      () =>
        electronApp.evaluate(({ webContents }) =>
          webContents.getAllWebContents().some((w) => w.getURL().includes('site=1')),
        ),
      { timeout: 15_000 },
    )
    .toBe(true);
  const paneId = (await gw.focused()).id;

  // Seed: a cookie + localStorage on THIS site (in-page), and a cookie for an
  // UNRELATED site set directly on the same partition.
  await electronApp.evaluate(async ({ webContents }) => {
    const wc = webContents.getAllWebContents().find((w) => w.getURL().includes('site=1'));
    if (!wc) throw new Error('live view not found');
    await wc.executeJavaScript(
      `document.cookie = 'gwtest=1; path=/'; localStorage.setItem('gwls', 'x'); true`,
    );
    await wc.session.cookies.set({
      url: 'http://unrelated.example.com/',
      name: 'other',
      value: 'keep',
    });
  });
  const cookieNames = () =>
    electronApp.evaluate(async ({ webContents }) => {
      const wc = webContents.getAllWebContents().find((w) => w.getURL().includes('site=1'));
      return (await wc!.session.cookies.get({})).map((c) => c.name).sort();
    });
  expect(await cookieNames()).toEqual(['gwtest', 'other']);

  // Clear the site (the same action the context-menu item invokes).
  await electronApp.evaluate(async (_e, pid: string) => {
    const reg = (globalThis as { __gwRegistry?: { clearSiteDataFor(id: string): Promise<void> } })
      .__gwRegistry;
    if (!reg) throw new Error('registry hook missing');
    await reg.clearSiteDataFor(pid);
  }, paneId);
  await window.waitForTimeout(1_000); // reload settles

  // This site's cookie and storage are gone; the unrelated cookie survives.
  expect(await cookieNames()).toEqual(['other']);
  const ls = await electronApp.evaluate(async ({ webContents }) => {
    const wc = webContents.getAllWebContents().find((w) => w.getURL().includes('site=1'));
    return (await wc!.executeJavaScript(`localStorage.getItem('gwls')`)) as string | null;
  });
  expect(ls, 'origin storage cleared').toBeNull();
});
