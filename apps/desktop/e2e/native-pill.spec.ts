import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// The pane name lives in the bottom bar's current crumb, which is reserved
// layout outside every WebContentsView's rect, so renaming and pane-zooming a
// live url pane work through the plain DOM input with no native view of their
// own. This spec pins rename and zoom over a live pane and checks that no
// name-pill webContents exists.

test('the bar crumb renames and zooms a live url pane; no native pill exists', async ({
  electronApp,
  window,
  gw,
}) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);

  const wcBefore = await electronApp.evaluate(
    ({ webContents }) => webContents.getAllWebContents().length,
  );
  await gw.openMenu();
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

  // There is no native name pill: no data: page carrying setLabel.
  const pillCount = await electronApp.evaluate(({ webContents }) =>
    webContents
      .getAllWebContents()
      .filter((w) => w.getURL().startsWith('data:text/html') && w.getURL().includes('setLabel'))
      .length,
  );
  expect(pillCount, 'no native name-pill view may exist').toBe(0);

  // The crumb reads "unnamed" until the user sets a name.
  await expect.poll(async () => (await gw.barName()).label, { timeout: 10_000 }).toBe('unnamed');
  await gw.clickBarName('right');
  const input = window.locator('#gw-rename-input');
  await expect(input).toBeVisible();
  await input.fill('cloud-console');
  await input.press('Enter');
  await expect
    .poll(async () => String(tileAt(await gw.getGrid(home.gridID), 'url', cx, cy)?.altText ?? ''))
    .toBe('cloud-console');
  await expect.poll(async () => (await gw.barName()).label).toBe('cloud-console');

  // Split first so the zoom is observable.
  await gw.splitFocusedPaneVertical();
  const twoPanes = await gw.panes();
  expect(twoPanes).toHaveLength(2);
  // A click on the live pane's center is a forwarded left press.
  const urlPane = twoPanes.slice().sort((a, b) => a.x - b.x)[0];
  await gw.clickScreen(urlPane.x + urlPane.w / 2, urlPane.y + urlPane.h / 2);
  await expect.poll(async () => (await gw.focused()).id).toBe(urlPane.id);
  await gw.clickBarName();
  await expect.poll(async () => (await gw.panes()).length, { timeout: 5_000 }).toBe(1);
  await gw.clickBarName();
  await expect.poll(async () => (await gw.panes()).length, { timeout: 5_000 }).toBe(2);
});
