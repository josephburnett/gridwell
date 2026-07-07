import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Issue #118: over a LIVE url pane the name bubble is a NATIVE view (DOM
// cannot paint above a WebContentsView). It shows the bubble label, LEFT-click
// opens the rename input (the view parks so the DOM input is usable), and
// RIGHT-click toggles the pane zoom — same contract as the DOM pill.

test('the native bubble over a live url pane renames and zooms', async ({
  electronApp,
  window,
  gw,
}) => {
  await gw.enterPlugin('localdb');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);

  // A placed url tile, auto-descended and live.
  const wcBefore = await electronApp.evaluate(
    ({ webContents }) => webContents.getAllWebContents().length,
  );
  await gw.openPalette();
  await gw.dragCreate('url', cx, cy);
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/?pill=1`);
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), {
      timeout: 15_000,
    })
    .toBeGreaterThan(wcBefore);
  await window.waitForTimeout(800); // let the label push land

  // The native pill exists and carries the bubble label ("unnamed" — the
  // tile has no user name yet).
  const pillText = () =>
    electronApp.evaluate(async ({ webContents }) => {
      const pill = webContents
        .getAllWebContents()
        .find((w) => w.getURL().startsWith('data:text/html') && w.getURL().includes('setLabel'));
      if (!pill) return null;
      return (await pill.executeJavaScript('document.getElementById("p").textContent')) as string;
    });
  await expect.poll(pillText, { timeout: 10_000 }).toBe('unnamed');

  // LEFT-click the pill: the view parks and the DOM rename input opens.
  const clickPill = (button: 'left' | 'right') =>
    electronApp.evaluate(async ({ webContents }, btn: string) => {
      const pill = webContents
        .getAllWebContents()
        .find((w) => w.getURL().startsWith('data:text/html') && w.getURL().includes('setLabel'));
      if (!pill) throw new Error('native pill not found');
      pill.sendInputEvent({ type: 'mouseDown', x: 120, y: 12, button: btn, clickCount: 1 } as never);
      pill.sendInputEvent({ type: 'mouseUp', x: 120, y: 12, button: btn, clickCount: 1 } as never);
    }, button);
  await clickPill('left');
  const input = window.locator('#gw-rename-input');
  await expect(input).toBeVisible();
  await input.fill('cloud-console');
  await input.press('Enter');
  await expect
    .poll(async () => String(tileAt(await gw.getGrid(home.gridID), 'url', cx, cy)?.altText ?? ''))
    .toBe('cloud-console');
  await expect.poll(pillText, { timeout: 10_000 }).toBe('cloud-console');

  // RIGHT-click the pill: the pane zooms (split first so it is observable).
  await gw.splitFocusedPaneVertical();
  const twoPanes = await gw.panes();
  expect(twoPanes).toHaveLength(2);
  // Focus back to the url pane by clicking its center (forwarded left).
  const urlPane = twoPanes.slice().sort((a, b) => a.x - b.x)[0];
  await gw.clickScreen(urlPane.x + urlPane.w / 2, urlPane.y + urlPane.h / 2);
  await expect.poll(async () => (await gw.focused()).id).toBe(urlPane.id);
  await clickPill('right');
  await expect.poll(async () => (await gw.panes()).length, { timeout: 5_000 }).toBe(1);
  await clickPill('right');
  await expect.poll(async () => (await gw.panes()).length, { timeout: 5_000 }).toBe(2);
});
