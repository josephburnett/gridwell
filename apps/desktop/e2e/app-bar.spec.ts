import { test, expect } from './fixtures';

// One navigation bar, across the bottom of the whole window, always visible,
// wearing whichever pane has focus. It is reserved layout: every pane ends at
// the band's top edge, whatever the split, so nothing can occlude it and no
// pane pays for it twice. A click in an unfocused pane moves focus and
// nothing else — and the bar follows, switching to that pane's chain and
// title without moving a pixel.

test('one bar spans the window, below every pane, and follows focus', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const winW = await window.evaluate(() => globalThis.innerWidth);
  const bar1 = await gw.bar();
  expect(bar1.left, 'the band starts at the window edge').toBe(0);
  expect(bar1.width, 'and runs the full window width').toBe(winW);
  expect(bar1.segments.length, 'wearing the focused pane, chain and all').toBeGreaterThan(0);

  await gw.splitFocusedPaneVertical();
  const panes = await gw.panes();
  expect(panes.length).toBe(2);
  const focused = panes.find((p) => p.focused)!;
  const other = panes.find((p) => !p.focused)!;

  // Still one band, still full width: a split adds panes, never bars.
  const bar2 = await gw.bar();
  expect(bar2.left).toBe(0);
  expect(bar2.width, 'two panes, one bar').toBe(winW);
  expect(bar2.top, 'and it did not move').toBe(bar1.top);

  // Every pane ends exactly at the band's top edge: the space is reserved
  // once, for the window, not once per pane.
  for (const p of panes) {
    expect(p.y + p.h, `pane ${p.id} ends at the band`).toBe(bar2.top);
  }

  // A left-click in the unfocused pane's empty center moves focus and nothing
  // else: no zoom toggle, no ascent, no pane closed.
  await gw.focusPane(other);
  await expect.poll(async () => (await gw.panes()).find((p) => p.focused)?.id).toBe(other.id);
  expect((await gw.panes()).length, 'no pane closed, none zoomed').toBe(2);

  // The bar now wears the newly focused pane — same band, same geometry.
  const bar3 = await gw.bar();
  expect(bar3.top).toBe(bar2.top);
  expect(bar3.left).toBe(0);
  expect(bar3.width).toBe(winW);
  expect(bar3.segments.length).toBeGreaterThan(0);
  const title = await gw.barName();
  expect(title.top, 'the title rides the one band').toBe(bar3.top);

  // And the panes did not resize when focus moved: things stay as you left
  // them.
  for (const before of [focused, other]) {
    const after = (await gw.panes()).find((p) => p.id === before.id)!;
    expect(after.h, `pane ${before.id} kept its height`).toBe(before.h);
    expect(after.y).toBe(before.y);
  }
});
