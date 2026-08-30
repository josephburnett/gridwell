import { test, expect } from './fixtures';

// Left-clicking a pane with a live url WebContentsView must transfer pane focus
// to that pane, closing the + menu on the previously-focused pane if it was
// open.
//
// Chromium's WebContentsView swallows the left-click, so the canvas onMouseDown,
// the only path calling menu.SyncFocus, never runs. urlview-preload.ts therefore
// sends VIEW_LEFTDOWN on every left-down, without preventDefault so in-page
// interaction stays with the page. Main relays it as EV.leftForward, and the
// wasm onForwardedLeftDown calls focusToPane, which does SetFocus,
// menu.TransferFocus to close the + menu, and refreshFileOverlay.
//
// onForwardedRightDown routes through the same focusToPane, so the two buttons
// cannot drift on the focus rules (see wasm/right_button.go).

test('left-clicking a live URL pane transfers focus when the palette is closed', async ({
  electronApp,
  window,
  gw,
}) => {
  await gw.enterPlugin('home');

  // Create a live url view through the ephemeral-visit swatch: click, not drag.
  const wcBefore = await electronApp.evaluate(
    ({ webContents }) => webContents.getAllWebContents().length,
  );
  await gw.clickPaletteSwatch('url');
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', 'https://example.com/focus-regression');
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();

  // Wait for the native WebContentsView to come up; create, descend, and open
  // are async.
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), {
      timeout: 15_000,
    })
    .toBeGreaterThan(wcBefore);

  const urlPaneId = (await gw.focused()).id;

  // Split the url pane: focus moves to the new right pane, and the url pane
  // keeps its live view on the left while losing focus.
  await gw.splitFocusedPaneVertical();
  const textPaneId = (await gw.focused()).id;
  expect(textPaneId, 'split moved focus off the URL pane').not.toBe(urlPaneId);

  // Left-click the url pane. The live view is not parked, since no gesture runs
  // and no palette is open, so the click goes to the native WebContentsView.
  // Chromium swallows it, but the preload fires VIEW_LEFTDOWN, main relays
  // EV.leftForward, and the wasm onForwardedLeftDown calls focusToPane. Without
  // that relay the click is lost and focus stays on the text pane.
  const urlPane = (await gw.panes()).find((p) => p.id === urlPaneId)!;
  await gw.clickScreen(urlPane.x + urlPane.w / 2, urlPane.y + urlPane.h / 2);

  // Focus must now be on the url pane. The circle control lives in the bottom
  // bar, outside every view, so there is no native per-pane control to poll.
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

  // Create a live url view.
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

  // Split to get a text pane; focus is on the new text pane.
  await gw.splitFocusedPaneVertical();
  const textPaneId = (await gw.focused()).id;
  expect(textPaneId).not.toBe(urlPaneId);

  // Open the + palette on the text pane. While it is open the live url view is
  // parked, since liveOverlaysHidden is true, and clicks go to the canvas. The
  // canvas onMouseDown calls focusToPane, which calls menu.TransferFocus and
  // closes the palette, so the canvas path goes through focusToPane too.
  await gw.openPalette();
  expect((await gw.palette()).open, 'palette open on the text pane').toBe(true);

  // Click the url pane. The live view is parked, since the palette is open, so
  // the click lands on the canvas and focusToPane closes the palette through
  // menu.TransferFocus.
  const urlPane = (await gw.panes()).find((p) => p.id === urlPaneId)!;
  await gw.clickScreen(urlPane.x + urlPane.w / 2, urlPane.y + urlPane.h / 2);
  await gw.waitIdle();

  // The palette must be closed, by menu.TransferFocus from focusToPane.
  expect((await gw.palette()).open, 'palette closed after focus moved to URL pane').toBe(false);

  // And focus is now on the url pane.
  expect((await gw.focused()).id, 'focus moved to the URL pane').toBe(urlPaneId);
});
