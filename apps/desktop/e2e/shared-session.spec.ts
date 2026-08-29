import { test, expect } from './fixtures';

// Owner decision 2026-07-26: ONE host-local Chromium session. Every live url
// tile — whichever plugin owns it, local or through a mount — browses on the
// same persistent partition, so your logins are your logins everywhere. This
// replaced the per-plugin partitions and their hydrate/dehydrate blob
// choreography (GetSession/PutSession), which daily-driver use falsified.
//
// The seam: a cookie set while live in PLUGIN A's tile is visible to a live
// tile owned by PLUGIN B. Under the old model these were different
// partitions by construction and this spec would fail.

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

  // A live tile in the FIRST plugin sets a cookie in-page.
  await gw.enterPlugin('home');
  await goLiveURL('plug=one');
  await electronApp.evaluate(async ({ webContents }) => {
    const wc = webContents.getAllWebContents().find((w) => w.getURL().includes('plug=one'));
    if (!wc) throw new Error('first live view not found');
    await wc.executeJavaScript(`document.cookie = 'gwshared=yes; path=/'; true`);
  });

  // Ascend out (freezes + tears the view down), portal home, then enter the
  // SECOND plugin and go live there.
  await gw.ascendViaCrumb(); // ascend the url descent
  await gw.ascendViaCrumb(); // ascend the plugin portal
  await gw.enterPlugin('second');
  await goLiveURL('plug=two');

  // The second plugin's live view sees the first's cookie: one session.
  const cookie = await electronApp.evaluate(async ({ webContents }) => {
    const wc = webContents.getAllWebContents().find((w) => w.getURL().includes('plug=two'));
    if (!wc) throw new Error('second live view not found');
    return (await wc.executeJavaScript(`document.cookie`)) as string;
  });
  expect(cookie, 'the cookie crossed the plugin boundary — one host-local session').toContain(
    'gwshared=yes',
  );
});
