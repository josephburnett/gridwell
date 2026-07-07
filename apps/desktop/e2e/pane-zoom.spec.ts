import { test, expect } from './fixtures';

// Issues #80 + #118: RIGHT-click on the name bubble zooms the focused pane to
// the full window, tmux-style; right-click again restores the exact prior
// layout (split ratios untouched — the guiding rule). The bubble is the
// pane's one universal handle; the old double-right-click gesture is gone.

async function rightClickBubble(window: any) {
  const pill = window.locator('#gw-rename-pill');
  await pill.waitFor({ state: 'visible', timeout: 5_000 });
  await pill.dispatchEvent('mousedown', { button: 2 });
}

test('right-clicking the bubble zooms a pane and back, restoring the layout exactly', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('localdb');
  await gw.splitFocusedPaneVertical();
  const before = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  expect(before).toHaveLength(2);
  const focusedBefore = before.find((p) => p.focused)!;

  // Zoom the focused pane via its bubble: it owns the whole layout.
  await rightClickBubble(window);
  await gw.waitIdle();
  const zoomed = await gw.panes();
  expect(zoomed, 'only the zoomed pane is laid out').toHaveLength(1);
  expect(zoomed[0].id).toBe(focusedBefore.id);
  expect(zoomed[0].w, 'zoomed pane spans the full width').toBeGreaterThan(focusedBefore.w * 1.5);

  // Unzoom: byte-identical layout.
  await rightClickBubble(window);
  await gw.waitIdle();
  const after = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  expect(after).toHaveLength(2);
  for (let i = 0; i < 2; i++) {
    expect(after[i].id).toBe(before[i].id);
    expect(after[i].x).toBeCloseTo(before[i].x, 3);
    expect(after[i].w).toBeCloseTo(before[i].w, 3);
  }
});

test('the bubble shows a read-only context label on non-renamable panes', async ({
  gw,
  window,
}) => {
  // The node grid (the landing page) is "home"; a plugin root shows the
  // plugin's config label. Neither opens the rename input on left-click.
  const pill = window.locator('#gw-rename-pill');
  await expect(pill).toHaveText('home');
  await pill.dispatchEvent('mousedown', { button: 0 });
  await expect(window.locator('#gw-rename-input')).toHaveCount(0);

  await gw.enterPlugin('localdb');
  await expect(pill).toHaveText('e2e'); // the seeded plugin's name
  await pill.dispatchEvent('mousedown', { button: 0 });
  await expect(window.locator('#gw-rename-input')).toHaveCount(0);
});
