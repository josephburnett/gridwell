import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Issue #113: a url tile's navigation back-stack survives freeze/revive. The
// freeze captures navigationHistory (urls+titles, capped) into the tile's
// url_history via SetURLState; going live again restores it via
// navigationHistory.restore — so the < button still works after ascending
// and coming back.

test('a revived url tile can still go back', async ({ electronApp, window, gw }) => {
  await gw.enterPlugin('localdb');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);

  // A PLACED url tile (drag-create prompts for the url), which auto-descends
  // and goes live on the local origin.
  const wcBefore = await electronApp.evaluate(
    ({ webContents }) => webContents.getAllWebContents().length,
  );
  await gw.openPalette();
  await gw.dragCreate('url', cx, cy);
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?h=1`);
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), {
      timeout: 15_000,
    })
    .toBeGreaterThan(wcBefore);

  // Navigate twice INSIDE the live view — real navigations, real history.
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

  // Revive: descend (frozen) — the auto-go-live only fires on creation, so
  // click the corner circle to go live — and the restored view can go BACK.
  await gw.descendCell(cx, cy);
  await gw.waitIdle();
  const pal = await gw.palette();
  await window.mouse.click(pal.plusX, pal.plusY); // frozen url corner = go live
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), {
      timeout: 15_000,
    })
    .toBeGreaterThan(wcBefore);

  const back = await electronApp.evaluate(async ({ webContents }) => {
    const wc = webContents.getAllWebContents().find((w) => w.getURL().includes('h='));
    if (!wc) return { canGoBack: false, landed: '' };
    const nav = wc.navigationHistory;
    const canGoBack = nav.canGoBack();
    if (canGoBack) nav.goBack();
    await new Promise((r) => setTimeout(r, 800));
    return { canGoBack, landed: wc.getURL() };
  });
  expect(back.canGoBack, 'restored view has a back-stack').toBe(true);
  expect(back.landed, 'back lands on the prior page').toContain('h=2');
});
