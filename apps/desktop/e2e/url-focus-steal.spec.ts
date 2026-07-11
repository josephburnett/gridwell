import { test, expect } from './fixtures';

// Issue #172: a live url view whose page refreshes itself must NEVER grab OS
// keyboard focus from the pane the user is typing in. Chromium implicitly
// focuses a WebContentsView's new document widget on a page-initiated
// navigation (verified empirically: before the guard, one location.reload()
// flipped the view to focused and it stayed there); Gridwell itself never
// calls focus() on a view. The guard: a navigation that lands focus on an
// UNFOCUSED pane's view hands focus straight back to the root webContents.

test('a self-reloading url view never keeps stolen focus', async ({
  electronApp,
  window,
  gw,
}) => {
  await gw.enterPlugin('localdb');
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

  // Move wasm focus off the url pane: split (the clone ascends the file
  // level — live views can't duplicate), then CLICK the grid pane so it is
  // unambiguously the focused pane — "the user is typing somewhere else".
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

  // The page refreshes itself repeatedly; after every reload the view must
  // NOT hold OS keyboard focus (before the guard it flipped to true on the
  // first reload and stayed).
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
