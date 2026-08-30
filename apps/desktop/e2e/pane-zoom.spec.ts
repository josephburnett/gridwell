import { test, expect } from './fixtures';

// A left-click on the bar's centered title zooms the focused pane to the full
// window, tmux-style, and a second click restores the exact prior layout with
// the split ratios untouched. The title is the pane's one universal handle; a
// right-click on it renames.

test('clicking the bar title zooms a pane and back, restoring the layout exactly', async ({
  gw,
}) => {
  await gw.enterPlugin('home');
  await gw.splitFocusedPaneVertical();
  const before = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  expect(before).toHaveLength(2);
  const focusedBefore = before.find((p) => p.focused)!;

  // Zoom the focused pane through the title: it owns the whole layout, and the
  // title's zoom marker says so.
  await gw.clickBarName();
  await gw.waitIdle();
  const zoomed = await gw.panes();
  expect(zoomed, 'only the zoomed pane is laid out').toHaveLength(1);
  expect(zoomed[0].id).toBe(focusedBefore.id);
  expect(zoomed[0].w, 'zoomed pane spans the full width').toBeGreaterThan(focusedBefore.w * 1.5);
  expect((await gw.barName()).label).toContain('⛶');

  // Unzoom: byte-identical layout.
  await gw.clickBarName();
  await gw.waitIdle();
  const after = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  expect(after).toHaveLength(2);
  expect((await gw.barName()).label).not.toContain('⛶');
  for (let i = 0; i < 2; i++) {
    expect(after[i].id).toBe(before[i].id);
    expect(after[i].x).toBeCloseTo(before[i].x, 3);
    expect(after[i].w).toBeCloseTo(before[i].w, 3);
  }
});

test('the title shows a read-only context label on non-renamable panes', async ({
  gw,
  window,
}) => {
  // A root grid shows its configured label, and right-clicking it never opens
  // the rename input.
  await gw.plugins(); // wait for boot to settle on the root grid
  await expect.poll(async () => (await gw.barName()).label).toBe('home'); // the home's name
  expect((await gw.barName()).editable).toBe(false);
  await gw.clickBarName('right');
  await expect(window.locator('#gw-rename-input')).toHaveCount(0);
});
