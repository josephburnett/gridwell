import { test, expect } from './fixtures';

// One bar at the bottom of the window, as wide as the focused pane and sitting
// under it. The band it sits in is full width and reserved once, so every pane
// ends at its top edge and no pane resizes when focus moves. Beside the bar the
// band belongs to no pane, and a click there does nothing and does not fall
// through.

test('one bar rides the focused pane, in a band reserved once', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const winW = await window.evaluate(() => globalThis.innerWidth);
  const bar1 = await gw.bar();
  const solo = (await gw.panes())[0];
  expect(bar1.left, 'the lone pane spans the window, so the bar does too').toBe(solo.x);
  expect(bar1.width).toBe(solo.w);
  expect(bar1.width).toBe(winW);
  expect(bar1.segments.length, 'riding the focused pane, chain and all').toBeGreaterThan(0);

  await gw.splitFocusedPaneVertical();
  const panes = await gw.panes();
  expect(panes.length).toBe(2);
  const focused = panes.find((p) => p.focused)!;
  const other = panes.find((p) => !p.focused)!;

  const bar2 = await gw.bar();
  expect(bar2.left, 'the bar sits under the focused pane').toBe(focused.x);
  expect(bar2.width, 'and is only as wide as it').toBe(focused.w);
  expect(bar2.width, 'so a split narrows the chrome').toBeLessThan(winW);
  expect(bar2.top, 'the band did not move').toBe(bar1.top);
  expect(bar2.height, 'nor change height').toBe(bar1.height);

  for (const p of panes) {
    expect(p.y + p.h, `pane ${p.id} ends at the band`).toBe(bar2.top);
  }

  // The + menu rides the bar, so it sits at the focused pane's right edge.
  const pal = await gw.palette();
  expect(pal.plusX, 'the + menu is beside the pane you are working in').toBeGreaterThan(focused.x);
  expect(pal.plusX).toBeLessThan(focused.x + focused.w);
  expect(pal.plusY).toBeGreaterThan(bar2.top);

  await gw.clickScreen(other.x + other.w - 8, bar2.top + bar2.height / 2);
  expect((await gw.palette()).open, 'no + menu beside the bar').toBe(false);
  const afterQuiet = await gw.panes();
  expect(afterQuiet.find((p) => p.focused)?.id, 'focus stayed put').toBe(focused.id);
  expect(afterQuiet.length, 'no pane closed, none zoomed').toBe(2);
  for (const before of [focused, other]) {
    const after = afterQuiet.find((p) => p.id === before.id)!;
    expect(after.x, `pane ${before.id} unmoved`).toBe(before.x);
    expect(after.w).toBe(before.w);
  }

  // A click on nothing in an unfocused pane moves focus, and the bar follows.
  await gw.focusPane(other);
  await expect.poll(async () => (await gw.panes()).find((p) => p.focused)?.id).toBe(other.id);
  expect((await gw.panes()).length, 'no pane closed, none zoomed').toBe(2);

  const bar3 = await gw.bar();
  expect(bar3.left, 'the bar slid under the newly focused pane').toBe(other.x);
  expect(bar3.width).toBe(other.w);
  expect(bar3.top, 'the band is where it was').toBe(bar2.top);
  expect(bar3.height).toBe(bar2.height);
  expect(bar3.segments.length).toBeGreaterThan(0);
  const title = await gw.barName();
  expect(title.top, 'the title rides the one bar').toBe(bar3.top);
  expect(title.x, 'and is inside it').toBeGreaterThanOrEqual(bar3.left);
  expect(title.x + title.w).toBeLessThanOrEqual(bar3.left + bar3.width);

  for (const before of [focused, other]) {
    const after = (await gw.panes()).find((p) => p.id === before.id)!;
    expect(after.h, `pane ${before.id} kept its height`).toBe(before.h);
    expect(after.y).toBe(before.y);
    expect(after.x, `pane ${before.id} kept its column`).toBe(before.x);
    expect(after.w).toBe(before.w);
  }
});
