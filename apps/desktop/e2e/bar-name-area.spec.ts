import { test, expect } from './fixtures';

// The name's room in the bar, between the crumbs and the circle, is the
// name for a left click: pressing the empty space beside the text zooms the
// pane like pressing the text. Rename stays on the text alone, so a right
// click beside it opens nothing (wsbar.RouteClick).

test('a left click anywhere in the name area zooms; only the name itself renames', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);
  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  await gw.descendCell(cx, cy);
  await gw.waitIdle();
  await gw.splitFocusedPaneVertical();
  await gw.waitIdle();
  expect(await gw.panes()).toHaveLength(2);
  await expect.poll(async () => (await gw.barName()).editable).toBe(true);

  // A point in the name area well clear of the drawn title, toward the circle.
  const emptyPoint = async () => {
    const bar = await gw.bar();
    const t = await gw.barName();
    const slotLeft = bar.left + bar.width - 48;
    const x = (t.x + t.w + slotLeft) / 2;
    expect(x - (t.x + t.w), 'the point is off the title').toBeGreaterThan(8);
    expect(slotLeft - x, 'the point is off the circle').toBeGreaterThan(8);
    return { x, y: bar.top + bar.height / 2 };
  };

  const off = await emptyPoint();
  await window.mouse.click(off.x, off.y);
  await gw.waitIdle();
  expect(await gw.panes(), 'the empty area zooms like the name').toHaveLength(1);
  expect((await gw.barName()).label).toContain('⛶');

  const back = await emptyPoint();
  await window.mouse.click(back.x, back.y);
  await gw.waitIdle();
  expect(await gw.panes(), 'and unzooms').toHaveLength(2);
  expect((await gw.barName()).label).not.toContain('⛶');

  const input = window.locator('#gw-rename-input');
  const right = await emptyPoint();
  await window.mouse.click(right.x, right.y, { button: 'right' });
  await gw.waitIdle();
  await expect(input, 'a right click beside the name never renames').toHaveCount(0);
  expect(await gw.panes(), 'nor zooms').toHaveLength(2);

  await gw.clickBarName('right');
  await expect(input, 'a right click on the name renames').toBeVisible();
  await input.press('Escape');
  await expect(input).toHaveCount(0);
});
