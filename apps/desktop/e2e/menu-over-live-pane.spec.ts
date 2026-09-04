import { test, expect } from './fixtures';

// The + menu's popover is anchored to the bar slot at the bottom of the window,
// so on a stacked layout it floats over the pane BELOW the focused one. When
// that lower pane is descended into a live url, its WebContentsView covers the
// same pixels the swatches are drawn on.
//
// The press was already routed by the menu's own pane (onMouseDown claims a
// left-click inside the open palette before pane resolution). The release was
// not: onMouseUp swallowed any left release over a live url pane's content box
// before it looked at the armed gesture. The swatch's mousedown armed the
// template drag, its mouseup was discarded, and everything downstream followed
// from a gesture that could never end — no url modal, an immortal ghost painted
// at raw screen coords, and every live view parked forever, because the park
// (liveOverlaysHidden) is keyed off the same armed drag. waitIdle, which
// includes dragging == nil, never returned.
//
// The fix gives that decision one owner, panebox.LiveViewOwnsPoint, which asks
// whether the view is parked: an armed gesture parks every live view, so the
// release that ends one is never the view's to swallow.
test('the + menu over a stacked live url pane keeps its own release', async ({
  electronApp,
  window,
  gw,
}) => {
  await gw.enterPlugin('home');

  // Two stacked panes.
  await gw.splitFocusedPaneHorizontal();
  const stacked = (await gw.panes()).slice().sort((a, b) => a.y - b.y);
  expect(stacked.length, 'the split made two stacked panes').toBe(2);
  const upperId = stacked[0].id;
  const lowerId = stacked[1].id;

  // The lower pane goes live on a url, served from the node's own origin so it
  // loads with no network.
  await gw.focusPane(stacked[1]);
  const wcBefore = await electronApp.evaluate(
    ({ webContents }) => webContents.getAllWebContents().length,
  );
  await gw.clickPaletteSwatch('url');
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/?menu-over-live=1`);
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), {
      timeout: 15_000,
    })
    .toBeGreaterThan(wcBefore);
  expect((await gw.focused()).id, 'the lower pane holds the live visit').toBe(lowerId);

  // Focus the upper pane and open its + menu there.
  const upper = (await gw.panes()).find((p) => p.id === upperId)!;
  await gw.focusPane(upper);
  expect((await gw.focused()).id, 'the upper pane has focus').toBe(upperId);
  await gw.openPalette();

  const pal = await gw.palette();
  const swatch = pal.items.find((i) => !i.isPlugin && i.kind === 'url');
  expect(swatch, 'the url swatch is in the palette').toBeTruthy();
  const cx = swatch!.x + swatch!.w / 2;
  const cy = swatch!.y + swatch!.h / 2;

  // The configuration this spec exists for: the swatch is drawn over the live
  // lower pane. Without this the click would land on plain canvas and the
  // regression could not reproduce.
  const lower = (await gw.panes()).find((p) => p.id === lowerId)!;
  expect(cy, 'the url swatch floats over the live lower pane').toBeGreaterThan(lower.y);
  expect(cy, 'and inside it').toBeLessThan(lower.y + lower.h);

  // Click the swatch: an ephemeral-visit click, which opens the url modal.
  await window.mouse.click(cx, cy);

  // The release resolved the gesture: nothing is left armed. Polled first and
  // on its own budget, so the failure names the stuck drag rather than timing
  // out inside waitIdle.
  await expect
    .poll(() => window.evaluate(() => (window as any).__gridwellTest.idleDetail().dragging), {
      message: 'the swatch release must resolve the armed template drag',
      timeout: 5_000,
    })
    .toBe(false);
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await gw.waitIdle();

  // Dismiss the modal: the click was the gesture under test, not the visit.
  await window.keyboard.press('Escape');
  await expect(window.locator('#gw-url-modal.open')).toHaveCount(0);
  await gw.waitIdle();

  // The menu's click acted in the menu's pane, never the one under the cursor.
  expect((await gw.focused()).id, 'focus stayed on the upper pane').toBe(upperId);
  const errs = await window.evaluate(() => (window as any).__gridwellTest.errors());
  expect(errs.notices, 'nothing surfaced from the menu click').toHaveLength(0);
});
