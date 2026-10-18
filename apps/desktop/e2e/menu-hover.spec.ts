import { test, expect } from './fixtures';

// The + menu's popover is anchored to the bar slot at the bottom of the window,
// so on a stacked layout its swatches are drawn over the pane below the focused
// one. Every swatch rect is laid out for the menu's own pane, so the hover
// hit-test has to route by the menu's pane rather than the pane under the
// cursor. menuPaneForPointer is the one router for hover and press both, and it
// cannot return a nil pane.
test('a swatch highlights when the popover floats over another pane', async ({ gw, window }) => {
  await gw.enterPlugin('home');

  await gw.splitFocusedPaneHorizontal();
  const stacked = (await gw.panes()).slice().sort((a, b) => a.y - b.y);
  expect(stacked.length, 'the split made two stacked panes').toBe(2);
  const [upper, lower] = stacked;

  // The menu opens on the upper pane; its popover is drawn over the lower one.
  await gw.focusPane(upper);
  expect((await gw.focused()).id, 'the upper pane has focus').toBe(upper.id);
  await gw.openMenu();

  const menu = await gw.menu();
  expect(menu.hover, 'a freshly opened menu hovers nothing').toBe(-1);
  const swatch = menu.items.find((i) => !i.isPlugin && i.kind === 'url');
  expect(swatch, 'the url swatch is in the menu').toBeTruthy();
  const cx = swatch!.x + swatch!.w / 2;
  const cy = swatch!.y + swatch!.h / 2;

  // The swatch is drawn over the pane that does not own the menu.
  expect(cy, 'the swatch is drawn over the lower pane').toBeGreaterThan(lower.y);
  expect(cy, 'and inside it').toBeLessThan(lower.y + lower.h);

  await window.mouse.move(cx, cy);
  await expect
    .poll(() => window.evaluate(() => (window as any).__gridwellTest.menu().hover), {
      message: 'the swatch under the cursor highlights',
      timeout: 5_000,
    })
    .toBe(swatch!.index);

  // Off the popover the hover clears, through the same router.
  await window.mouse.move(upper.x + upper.w / 2, upper.y + upper.h / 2);
  await expect
    .poll(() => window.evaluate(() => (window as any).__gridwellTest.menu().hover), {
      message: 'leaving the popover clears the hover',
      timeout: 5_000,
    })
    .toBe(-1);

  // Hovering moves neither the menu nor focus.
  const after = await gw.menu();
  expect(after.open, 'the menu stayed open').toBe(true);
  expect((await gw.focused()).id, 'focus stayed on the upper pane').toBe(upper.id);
});
