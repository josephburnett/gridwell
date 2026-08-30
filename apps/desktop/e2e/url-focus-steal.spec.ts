import { test, expect } from './fixtures';

// A live url view whose page refreshes itself must never take OS keyboard focus
// from the pane the user is typing in. Chromium focuses a WebContentsView's new
// document widget on a page-initiated navigation, and one location.reload() is
// enough to flip the view to focused and keep it there; Gridwell never calls
// focus() on a view itself. The guard: a navigation that lands focus on an
// unfocused pane's view hands focus straight back to the root webContents.

test('a self-reloading url view never keeps stolen focus', async ({
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
  await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?focussteal=1`);
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), {
      timeout: 15_000,
    })
    .toBeGreaterThan(wcBefore);

  // Move pane focus off the url pane: split, where the clone ascends the file
  // level because live views cannot duplicate, then click the grid pane so it is
  // unambiguously focused, standing in for the user typing somewhere else.
  await gw.splitFocusedPaneVertical();
  await gw.waitIdle();
  const other = (await gw.panes()).find((p) => p.textFocus === '')!;
  expect(other, 'the split produced a grid pane').toBeTruthy();
  await window.mouse.click(other.x + other.w / 2, other.y + other.h / 2);
  await gw.waitIdle();
  expect((await gw.focused()).id, 'the grid pane holds wasm focus').toBe(other.id);

  const viewFocused = () =>
    electronApp.evaluate(({ webContents }) => {
      const view = webContents
        .getAllWebContents()
        .find((w) => w.getURL().includes('focussteal=1'));
      return view?.isFocused() ?? null;
    });

  // The page refreshes itself repeatedly, and after every reload the view must
  // not hold OS keyboard focus. Unguarded it flips to focused on the first
  // reload and stays there.
  for (let i = 0; i < 3; i++) {
    await electronApp.evaluate(({ webContents }) => {
      const view = webContents
        .getAllWebContents()
        .find((w) => w.getURL().includes('focussteal=1'));
      return view?.executeJavaScript('location.reload(); true');
    });
    await expect
      .poll(viewFocused, {
        message: `reload ${i}: the view must not keep stolen focus`,
        timeout: 5_000,
      })
      .toBe(false);
    await window.waitForTimeout(300);
    expect(await viewFocused(), `reload ${i}: focus must not drift back`).toBe(false);
  }
});
