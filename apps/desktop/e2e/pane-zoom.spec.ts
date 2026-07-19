import { test, expect } from './fixtures';

// Issues #80 + #118: RIGHT-click on the name bubble zooms the focused pane to
// the full window, tmux-style; right-click again restores the exact prior
// layout (split ratios untouched — the guiding rule). The bubble is the
// pane's one universal handle; the old double-right-click gesture is gone.

async function rightClickBubble(window: any) {
  const pill = window.locator('#gw-rename-pill');
  await pill.waitFor({ state: 'visible', timeout: 5_000 });
  await pill.click({ button: 'right' }); // real mouse — see #130
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

  // Zoom the focused pane via its bubble: it owns the whole layout, and the
  // bubble says so (the zoom marker, issue #124).
  await rightClickBubble(window);
  await gw.waitIdle();
  const zoomed = await gw.panes();
  expect(zoomed, 'only the zoomed pane is laid out').toHaveLength(1);
  expect(zoomed[0].id).toBe(focusedBefore.id);
  expect(zoomed[0].w, 'zoomed pane spans the full width').toBeGreaterThan(focusedBefore.w * 1.5);
  await expect(window.locator('#gw-rename-pill')).toContainText('⛶');

  // Unzoom: byte-identical layout.
  await rightClickBubble(window);
  await gw.waitIdle();
  const after = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  expect(after).toHaveLength(2);
  await expect(window.locator('#gw-rename-pill')).not.toContainText('⛶');
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
  // A plugin root shows the plugin's config label (boot lands inside the
  // first plugin, so this is the boot pane) and never opens the rename input
  // on left-click.
  await gw.plugins(); // wait for boot to settle on the plugin root
  const pill = window.locator('#gw-rename-pill');
  await expect(pill).toHaveText('e2e'); // the seeded plugin's name
  await pill.click();
  await expect(window.locator('#gw-rename-input')).toHaveCount(0);
});
