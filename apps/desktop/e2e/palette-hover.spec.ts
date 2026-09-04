import { test, expect } from './fixtures';

// The + menu's popover is anchored to the bar slot at the bottom of the window,
// so on a stacked layout its swatches are drawn over the pane BELOW the focused
// one. Every swatch rect is laid out for the menu's own pane, so the hover
// hit-test has to route the same way the press does — by the menu's pane, not
// by the pane under the cursor. It did not: it looked up the pane at the
// pointer and hit-tested the palette against that, the exact inversion of the
// press. On a stacked layout that is always the wrong pane, so no swatch ever
// highlighted and the menu read dead under the cursor. (The same lookup could
// also hand back ok with a nil pane, which the next line dereferenced.)
//
// menuPaneForPointer is now the one router for both, and it cannot return a nil
// pane.
test('a swatch highlights when the popover floats over another pane', async ({ gw, window }) => {
  await gw.enterPlugin('home');

  await gw.splitFocusedPaneHorizontal();
  const stacked = (await gw.panes()).slice().sort((a, b) => a.y - b.y);
  expect(stacked.length, 'the split made two stacked panes').toBe(2);
  const [upper, lower] = stacked;

  // The menu opens on the upper pane; its popover is drawn over the lower one.
  await gw.focusPane(upper);
  expect((await gw.focused()).id, 'the upper pane has focus').toBe(upper.id);
  await gw.openPalette();

  const pal = await gw.palette();
  expect(pal.hover, 'a freshly opened palette hovers nothing').toBe(-1);
  const swatch = pal.items.find((i) => !i.isPlugin && i.kind === 'url');
  expect(swatch, 'the url swatch is in the palette').toBeTruthy();
  const cx = swatch!.x + swatch!.w / 2;
  const cy = swatch!.y + swatch!.h / 2;

  // The configuration this spec exists for: the swatch is over the OTHER pane.
  expect(cy, 'the swatch is drawn over the lower pane').toBeGreaterThan(lower.y);
  expect(cy, 'and inside it').toBeLessThan(lower.y + lower.h);

  await window.mouse.move(cx, cy);
  await expect
    .poll(() => window.evaluate(() => (window as any).__gridwellTest.palette().hover), {
      message: 'the swatch under the cursor highlights',
      timeout: 5_000,
    })
    .toBe(swatch!.index);

  // Off the popover and the hover clears — the same router, answering "no
  // swatch here" rather than "wrong pane".
  await window.mouse.move(upper.x + upper.w / 2, upper.y + upper.h / 2);
  await expect
    .poll(() => window.evaluate(() => (window as any).__gridwellTest.palette().hover), {
      message: 'leaving the popover clears the hover',
      timeout: 5_000,
    })
    .toBe(-1);

  // The menu is still open on its own pane: hovering moved nothing.
  const after = await gw.palette();
  expect(after.open, 'the menu stayed open').toBe(true);
  expect((await gw.focused()).id, 'focus stayed on the upper pane').toBe(upper.id);
});
