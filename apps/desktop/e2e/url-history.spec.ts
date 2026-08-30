import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// A url tile's navigation back-stack survives freeze and revive. The freeze
// captures navigationHistory, urls and titles, capped, into the tile's
// url_history, and going live again restores it through
// navigationHistory.restore, so the back button still works after ascending and
// coming back.

test('a revived url tile can still go back', async ({ electronApp, window, gw }) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);

  // A placed url tile: drag-create lands it bare, since the drop never prompts.
  // The first descent opens the address prompt, and submitting descends into the
  // live page on the local origin.
  const wcBefore = await electronApp.evaluate(
    ({ webContents }) => webContents.getAllWebContents().length,
  );
  await gw.openPalette();
  await gw.dragCreate('url', cx, cy);
  await gw.descendCell(cx, cy);
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?h=1`);
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();
  // Poll for the navigated view rather than a webContents count: the count grows
  // at view creation, before loadURL lands.
  await expect
    .poll(
      () =>
        electronApp.evaluate(({ webContents }) =>
          webContents.getAllWebContents().some((w) => w.getURL().includes('h=1')),
        ),
      { timeout: 15_000 },
    )
    .toBe(true);

  // Navigate twice inside the live view: real navigations, real history.
  const navTo = async (marker: string) => {
    await electronApp.evaluate(
      async ({ webContents }, [org, m]: string[]) => {
        const wc = webContents.getAllWebContents().find((w) => w.getURL().includes('h='));
        if (!wc) throw new Error('live view not found');
        await wc.executeJavaScript(`location.href = ${JSON.stringify(`${org}/wasm_exec.js?h=${m}`)}`);
      },
      [gw.origin, marker],
    );
    await expect
      .poll(() =>
        electronApp.evaluate(({ webContents }) => {
          const wc = webContents.getAllWebContents().find((w) => w.getURL().includes('h='));
          return wc?.getURL() ?? '';
        }),
      )
      .toContain(`h=${marker}`);
  };
  await navTo('2');
  await navTo('3');

  // Ascend: the freeze persists the back-stack on the tile.
  await gw.middleClickCell(cx, cy);
  await gw.waitIdle();
  await expect
    .poll(async () => String(tileAt(await gw.getGrid(home.gridID), 'url', cx, cy)?.urlHistory ?? ''), {
      timeout: 10_000,
    })
    .toContain('h=2');

  // Revive by descending. Every descent goes live, so the restored view appears
  // without a refresh click, and it can go back.
  await gw.descendCell(cx, cy);
  await gw.waitIdle();
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), {
      timeout: 15_000,
    })
    .toBeGreaterThan(wcBefore);

  // Wait for the restore's own navigation to commit before going back. A goBack
  // issued while the restore's load is still in flight is superseded and no-ops
  // silently, and canGoBack is already true the moment the entries install,
  // which is the trap.
  await expect
    .poll(
      () =>
        electronApp.evaluate(({ webContents }) => {
          const wc = webContents.getAllWebContents().find((w) => w.getURL().includes('h='));
          return wc ? { url: wc.getURL(), loading: wc.isLoading() } : { url: '', loading: true };
        }),
      { timeout: 15_000 },
    )
    .toMatchObject({ loading: false });

  const canGoBack = await electronApp.evaluate(({ webContents }) => {
    const wc = webContents.getAllWebContents().find((w) => w.getURL().includes('h='));
    if (!wc) return false;
    const nav = wc.navigationHistory;
    const can = nav.canGoBack();
    if (can) nav.goBack();
    return can;
  });
  expect(canGoBack, 'restored view has a back-stack').toBe(true);
  // Poll the landing: a fixed post-goBack sleep flaked under suite load, since
  // the navigation can take longer than any constant.
  await expect
    .poll(
      () =>
        electronApp.evaluate(({ webContents }) => {
          const wc = webContents.getAllWebContents().find((w) => w.getURL().includes('h='));
          return wc?.getURL() ?? '';
        }),
      { timeout: 15_000 },
    )
    .toContain('h=2');
});
