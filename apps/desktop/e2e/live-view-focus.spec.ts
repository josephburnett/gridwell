import { test, expect } from './fixtures';

// Left-clicking a pane with a live url WebContentsView transfers pane focus to
// it and closes the + menu on the pane that had focus. Chromium's
// WebContentsView swallows the left-click, so the canvas onMouseDown never
// runs: urlview-preload.ts sends VIEW_LEFTDOWN on every left-down without
// preventDefault, main relays it as EV.leftForward, and the wasm
// onForwardedLeftDown calls focusToPane. onForwardedRightDown routes through
// the same focusToPane, so the two buttons cannot drift apart on the focus
// rules (see wasm/right_button.go).

test('left-clicking a live URL pane transfers focus when the palette is closed', async ({
  electronApp,
  window,
  gw,
}) => {
  await gw.enterPlugin('home');

  // Clicking the swatch, rather than dragging it, opens an ephemeral visit.
  const wcBefore = await electronApp.evaluate(
    ({ webContents }) => webContents.getAllWebContents().length,
  );
  await gw.clickPaletteSwatch('url');
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', 'https://example.com/focus-regression');
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();

  // Create, descend, and open are async, so wait for the WebContentsView.
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), {
      timeout: 15_000,
    })
    .toBeGreaterThan(wcBefore);

  const urlPaneId = (await gw.focused()).id;

  // The split leaves the url pane live on the left and unfocused.
  await gw.splitFocusedPaneVertical();
  const textPaneId = (await gw.focused()).id;
  expect(textPaneId, 'split moved focus off the URL pane').not.toBe(urlPaneId);

  // No gesture runs and no palette is open, so the live view is not parked and
  // the click reaches the native WebContentsView. Only the VIEW_LEFTDOWN relay
  // can move focus from there.
  const urlPane = (await gw.panes()).find((p) => p.id === urlPaneId)!;
  await gw.clickScreen(urlPane.x + urlPane.w / 2, urlPane.y + urlPane.h / 2);

  // The circle control lives in the bottom bar, outside every view, so there is
  // no native per-pane control to poll for focus.
  await expect
    .poll(() => gw.focused().then((f) => f.id), { timeout: 5_000 })
    .toBe(urlPaneId);
});

test('left-clicking a live URL pane closes the + menu on the previously-focused pane', async ({
  electronApp,
  window,
  gw,
}) => {
  await gw.enterPlugin('home');

  const wcBefore = await electronApp.evaluate(
    ({ webContents }) => webContents.getAllWebContents().length,
  );
  await gw.clickPaletteSwatch('url');
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', 'https://example.com/menu-close');
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), {
      timeout: 15_000,
    })
    .toBeGreaterThan(wcBefore);

  const urlPaneId = (await gw.focused()).id;

  await gw.splitFocusedPaneVertical();
  const textPaneId = (await gw.focused()).id;
  expect(textPaneId).not.toBe(urlPaneId);

  // An open palette parks every live url view (pane.ParkSurface), so clicks
  // land on the canvas instead. The canvas path goes through the same
  // focusToPane.
  await gw.openPalette();
  expect((await gw.palette()).open, 'palette open on the text pane').toBe(true);

  const urlPane = (await gw.panes()).find((p) => p.id === urlPaneId)!;
  await gw.clickScreen(urlPane.x + urlPane.w / 2, urlPane.y + urlPane.h / 2);
  await gw.waitIdle();

  // focusToPane closes the palette through menu.TransferFocus.
  expect((await gw.palette()).open, 'palette closed after focus moved to URL pane').toBe(false);

  expect((await gw.focused()).id, 'focus moved to the URL pane').toBe(urlPaneId);
});
