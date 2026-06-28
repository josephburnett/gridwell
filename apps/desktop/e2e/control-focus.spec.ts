import { test, expect } from './fixtures';

// I9 / a real owner report: "the menu circle is still visible on url panes when
// not focused." The live URL view's corner control (back/ascend circle) is the
// one piece of per-pane chrome the native layer draws, and it must show only on
// the focused pane. This locks the wasm→native focus propagation end to end: the
// renderer feeds each view's focused flag (syncURLViews → bridgeSetHidden), and
// controlVisible(hidden, focused) parks the control when the pane isn't focused.
test('a live URL pane hides its corner control when it loses focus', async ({ electronApp, window, gw }) => {
  await gw.enterPlugin('localdb');

  // Ephemeral live-url visit: the focused pane descends into a native URL view.
  const wcBefore = await electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length);
  await gw.clickPaletteSwatch('url');
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', 'https://example.com/i9');
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), { timeout: 15_000 })
    .toBeGreaterThan(wcBefore);

  const urlPaneId = (await gw.focused()).id;
  const controlState = (paneId: string) =>
    electronApp.evaluate((_e, id) => (globalThis as any).__gwRegistry?.controlStateFor(id), paneId);

  // Focused URL pane: the corner control is on screen.
  await expect.poll(() => controlState(urlPaneId).then((s) => s?.visible), { timeout: 5_000 }).toBe(true);

  // Split → focus moves to the new pane; the URL pane loses focus.
  await gw.splitFocusedPaneVertical();
  expect((await gw.focused()).id, 'focus moved off the URL pane').not.toBe(urlPaneId);

  // The URL pane's corner control must hide: the renderer fed focused=false, and
  // controlVisible parks it. This is the "circle visible on an unfocused url
  // pane" regression guard.
  await expect.poll(() => controlState(urlPaneId).then((s) => s?.focused), { timeout: 5_000 }).toBe(false);
  await expect.poll(() => controlState(urlPaneId).then((s) => s?.visible), { timeout: 5_000 }).toBe(false);
});
