import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Issue #213 (superseding #118): the pane name lives in the bottom bar's
// CURRENT crumb — reserved layout OUTSIDE every WebContentsView's rect — so
// renaming and pane-zooming a LIVE url pane work through the plain DOM
// input with NO native pill view. This spec is the deletion's pin: the
// whole rename/zoom contract over a live pane, plus the assertion that no
// pill webContents exists anymore (exactly two data: pages per live pane:
// the content view's preload chrome aside, only the corner control).

test('the bar crumb renames and zooms a live url pane; no native pill exists', async ({
  electronApp,
  window,
  gw,
}) => {
  await gw.enterPlugin('localdb');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);

  // A placed url tile: dropped bare (#209), addressed at the first descent,
  // live after submit.
  const wcBefore = await electronApp.evaluate(
    ({ webContents }) => webContents.getAllWebContents().length,
  );
  await gw.openPalette();
  await gw.dragCreate('url', cx, cy);
  await gw.descendCell(cx, cy);
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?pill=1`);
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), {
      timeout: 15_000,
    })
    .toBeGreaterThan(wcBefore);

  // The native pill subsystem is GONE: no data: page carrying setLabel.
  const pillCount = await electronApp.evaluate(({ webContents }) =>
    webContents
      .getAllWebContents()
      .filter((w) => w.getURL().startsWith('data:text/html') && w.getURL().includes('setLabel'))
      .length,
  );
  expect(pillCount, 'no native name-pill view may exist').toBe(0);

  // The bar's current crumb carries the name ("unnamed" — no user name yet)
  // and the rename input works directly over the live pane: the bar is
  // outside the view's rect, no parking, no native forwarding.
  await expect.poll(async () => (await gw.barName()).label, { timeout: 10_000 }).toBe('unnamed');
  await gw.clickBarName();
  const input = window.locator('#gw-rename-input');
  await expect(input).toBeVisible();
  await input.fill('cloud-console');
  await input.press('Enter');
  await expect
    .poll(async () => String(tileAt(await gw.getGrid(home.gridID), 'url', cx, cy)?.altText ?? ''))
    .toBe('cloud-console');
  await expect.poll(async () => (await gw.barName()).label).toBe('cloud-console');

  // RIGHT-click the crumb: the pane zooms (split first so it is observable).
  await gw.splitFocusedPaneVertical();
  const twoPanes = await gw.panes();
  expect(twoPanes).toHaveLength(2);
  // Focus back to the url pane by clicking its center (forwarded left).
  const urlPane = twoPanes.slice().sort((a, b) => a.x - b.x)[0];
  await gw.clickScreen(urlPane.x + urlPane.w / 2, urlPane.y + urlPane.h / 2);
  await expect.poll(async () => (await gw.focused()).id).toBe(urlPane.id);
  await gw.clickBarName('right');
  await expect.poll(async () => (await gw.panes()).length, { timeout: 5_000 }).toBe(1);
  await gw.clickBarName('right');
  await expect.poll(async () => (await gw.panes()).length, { timeout: 5_000 }).toBe(2);
});
