import { test, expect } from './fixtures';

// A left click on the band beside the bar focuses the column above it, and the
// bar follows: of the panes stacked over that point, the one focused most
// recently (wsbar.BandFocus).

test('a click beside the bar focuses the most recent pane of the column above', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  await gw.splitFocusedPaneVertical();
  const two = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  expect(two.length).toBe(2);
  const [left, right] = two;

  await gw.focusPane(left);
  const bar1 = await gw.bar();
  expect(bar1.left, 'the bar is under the left pane').toBe(left.x);
  const bandY = bar1.top + bar1.height / 2;

  await gw.clickScreen(right.x + right.w / 2, bandY);
  await expect.poll(async () => (await gw.panes()).find((p) => p.focused)?.id).toBe(right.id);
  const bar2 = await gw.bar();
  expect(bar2.left, 'the bar slid under the right pane').toBe(right.x);
  expect(bar2.width).toBe(right.w);
  expect(
    await window.evaluate(() => document.activeElement?.tagName),
    'the keyboard went to the newly focused grid',
  ).toBe('CANVAS');

  await gw.splitFocusedPaneHorizontal();
  const three = await gw.panes();
  expect(three.length).toBe(3);
  const column = three.filter((p) => p.x === right.x).sort((a, b) => a.y - b.y);
  expect(column.length, 'the right column holds two stacked panes').toBe(2);
  const [top, bottom] = column;

  for (const p of [top, bottom, left]) await gw.focusPane(p);
  await expect.poll(async () => (await gw.panes()).find((p) => p.focused)?.id).toBe(left.id);
  await gw.clickScreen(right.x + right.w / 2, bandY);
  await expect
    .poll(async () => (await gw.panes()).find((p) => p.focused)?.id, 'the bottom was focused last')
    .toBe(bottom.id);
  expect((await gw.bar()).left).toBe(bottom.x);

  for (const p of [top, left]) await gw.focusPane(p);
  await gw.clickScreen(right.x + right.w / 2, bandY);
  await expect
    .poll(async () => (await gw.panes()).find((p) => p.focused)?.id, 'now the top was focused last')
    .toBe(top.id);

  const after = await gw.panes();
  expect(after.length, 'no pane closed, none zoomed').toBe(3);
  for (const before of three) {
    const now = after.find((p) => p.id === before.id)!;
    expect([now.x, now.y, now.w, now.h], `pane ${before.id} unmoved`).toEqual([before.x, before.y, before.w, before.h]);
  }
});
