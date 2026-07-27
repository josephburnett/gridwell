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
  // Poll for the NAVIGATED view, not a webContents count: the count grows at
  // view creation, before loadURL lands (the old count-poll rode the
  // session-hydrate await place() no longer performs — one local session,
  // 2026-07-26).
  await expect
    .poll(
      () =>
        electronApp.evaluate(({ webContents }) =>
          webContents.getAllWebContents().some((w) => w.getURL().includes('h=1')),
        ),
      { timeout: 15_000 },
    )
    .toBe(true);

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

  // Revive: descend — EVERY descent goes live now (issue #202), so the
  // restored view appears without a refresh click, and it can go BACK.
  await gw.descendCell(cx, cy);
  await gw.waitIdle();
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), {
      timeout: 15_000,
    })
    .toBeGreaterThan(wcBefore);

  // Wait for the RESTORE's own navigation to commit before going back — a
  // goBack issued while the restore's load is still in flight is superseded
  // and silently no-ops (canGoBack is already true the moment the entries
  // install, which is exactly the trap; captured from a real in-suite
  // failure trace).
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
  // POLL the landing — a fixed post-goBack sleep flaked under suite load
  // (the navigation can take longer than any constant you pick).
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
