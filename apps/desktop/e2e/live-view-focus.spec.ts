import { test, expect } from './fixtures';

// Regression guard for issue #34: left-clicking a pane with a live URL
// WebContentsView must transfer pane focus to that pane, closing the + menu on
// the previously-focused pane if it was open.
//
// Root cause: the preload forwarded only right-drag and middle-click to main;
// left-click was silently swallowed by Chromium's WebContentsView, so the wasm
// canvas's onMouseDown (the only path calling menu.SyncFocus) never ran.
//
// Fix: urlview-preload.ts now sends VIEW_LEFTDOWN on every left-down (without
// preventDefault — in-page interaction stays with the page). main relays it as
// EV.leftForward; the wasm handler onForwardedLeftDown calls focusToPane, which
// does SetFocus + menu.TransferFocus (closing the + menu) + refreshFileOverlay.
//
// Related: onForwardedRightDown had a latent twin — it duplicated the
// focus-transfer block but omitted SyncFocus. That is also fixed by routing
// through focusToPane (see wasm/right_button.go).

test('left-clicking a live URL pane transfers focus when the palette is closed', async ({
  electronApp,
  window,
  gw,
}) => {
  await gw.enterPlugin('localdb');

  // Create a live URL view via the ephemeral-visit swatch (click, not drag).
  const wcBefore = await electronApp.evaluate(
    ({ webContents }) => webContents.getAllWebContents().length,
  );
  await gw.clickPaletteSwatch('url');
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', 'https://example.com/focus-regression');
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();

  // Wait for the native WebContentsView to come up (async: create→descend→open).
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), {
      timeout: 15_000,
    })
    .toBeGreaterThan(wcBefore);

  const urlPaneId = (await gw.focused()).id;

  // Helper: read the native corner-control state for a pane from the registry.
  const controlState = (paneId: string) =>
    electronApp.evaluate(
      (_e, id) => (globalThis as any).__gwRegistry?.controlStateFor(id),
      paneId,
    );

  // The URL pane is focused: corner control must be visible.
  await expect
    .poll(() => controlState(urlPaneId).then((s) => s?.visible), { timeout: 5_000 })
    .toBe(true);

  // Split the URL pane — focus moves to the new right pane, the URL pane keeps
  // its live view on the left and loses focus.
  await gw.splitFocusedPaneVertical();
  const textPaneId = (await gw.focused()).id;
  expect(textPaneId, 'split moved focus off the URL pane').not.toBe(urlPaneId);

  // After the split the URL pane's corner control must be hidden (it lost focus).
  await expect
    .poll(() => controlState(urlPaneId).then((s) => s?.focused), { timeout: 5_000 })
    .toBe(false);
  await expect
    .poll(() => controlState(urlPaneId).then((s) => s?.visible), { timeout: 5_000 })
    .toBe(false);

  // LEFT-CLICK the URL pane. The live view is NOT parked (no gesture, no open
  // palette) so the click goes to the native WebContentsView — Chromium swallows
  // it, but the preload fires VIEW_LEFTDOWN → main → EV.leftForward → wasm
  // onForwardedLeftDown → focusToPane. Before the fix, this click was silently
  // lost and focus stayed on the text pane.
  const urlPane = (await gw.panes()).find((p) => p.id === urlPaneId)!;
  await gw.clickScreen(urlPane.x + urlPane.w / 2, urlPane.y + urlPane.h / 2);

  // Focus must now be on the URL pane — this is the primary regression assertion.
  await expect
    .poll(() => gw.focused().then((f) => f.id), { timeout: 5_000 })
    .toBe(urlPaneId);

  // The URL pane's corner control must now be visible (focus arrived there).
  await expect
    .poll(() => controlState(urlPaneId).then((s) => s?.focused), { timeout: 5_000 })
    .toBe(true);
  await expect
    .poll(() => controlState(urlPaneId).then((s) => s?.visible), { timeout: 5_000 })
    .toBe(true);
});

test('left-clicking a live URL pane closes the + menu on the previously-focused pane', async ({
  electronApp,
  window,
  gw,
}) => {
  await gw.enterPlugin('localdb');

  // Create a live URL view.
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

  // Open the + palette on the text pane. While the palette is open, the live URL
  // view is parked (liveOverlaysHidden=true) and clicks go to the canvas. The
  // canvas onMouseDown calls focusToPane → menu.TransferFocus, which closes the
  // palette. This tests that the canvas path also goes through focusToPane.
  await gw.openPalette();
  expect((await gw.palette()).open, 'palette open on the text pane').toBe(true);

  // Click the URL pane. The live view IS parked (palette open), so the click
  // lands on the canvas. focusToPane closes the palette via menu.TransferFocus.
  const urlPane = (await gw.panes()).find((p) => p.id === urlPaneId)!;
  await gw.clickScreen(urlPane.x + urlPane.w / 2, urlPane.y + urlPane.h / 2);
  await gw.waitIdle();

  // Palette must be closed (menu.TransferFocus fired from focusToPane).
  expect((await gw.palette()).open, 'palette closed after focus moved to URL pane').toBe(false);

  // And focus is now on the URL pane.
  expect((await gw.focused()).id, 'focus moved to the URL pane').toBe(urlPaneId);
});
