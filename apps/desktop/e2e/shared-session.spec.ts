import { test, expect } from './fixtures';

// One host-local Chromium session. Every live url tile, whichever namespace owns
// it, local or through a mount, browses on the same persistent partition, so a
// login holds everywhere.
//
// The seam: a cookie set while live in one plugin's tile is visible to a live
// tile owned by another. Per-plugin partitions would make these different
// sessions by construction and this spec would fail.

test.use({ extraNodes: ['second'] });

test('live tiles in different plugins share the one local session', async ({
  electronApp,
  window,
  gw,
}) => {
  const goLiveURL = async (marker: string) => {
    await gw.clickPaletteSwatch('url');
    await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
    await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?${marker}`);
    await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
    await gw.waitIdle();
    await expect
      .poll(
        () =>
          electronApp.evaluate(
            ({ webContents }, m: string) =>
              webContents.getAllWebContents().some((w) => w.getURL().includes(m)),
            marker,
          ),
        { timeout: 15_000 },
      )
      .toBe(true);
  };

  // A live tile in the first namespace sets a cookie in-page.
  await gw.enterPlugin('home');
  await goLiveURL('plug=one');
  await electronApp.evaluate(async ({ webContents }) => {
    const wc = webContents.getAllWebContents().find((w) => w.getURL().includes('plug=one'));
    if (!wc) throw new Error('first live view not found');
    await wc.executeJavaScript(`document.cookie = 'gwshared=yes; path=/'; true`);
  });

  // Ascend out, which freezes and tears the view down, portal home, then enter
  // the second namespace and go live there.
  await gw.ascendViaCrumb(); // ascend the url descent
  await gw.ascendViaCrumb(); // ascend the menu portal
  await gw.enterPlugin('second');
  await goLiveURL('plug=two');

  // The second live view sees the first's cookie: one session.
  const cookie = await electronApp.evaluate(async ({ webContents }) => {
    const wc = webContents.getAllWebContents().find((w) => w.getURL().includes('plug=two'));
    if (!wc) throw new Error('second live view not found');
    return (await wc.executeJavaScript(`document.cookie`)) as string;
  });
  expect(cookie, 'the cookie crossed the plugin boundary — one host-local session').toContain(
    'gwshared=yes',
  );
});
