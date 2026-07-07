import { test, expect } from './fixtures';

// Issue #80: double RIGHT-click on a pane interior zooms it to the full
// window, tmux-style; double right-click again restores the exact prior
// layout (split ratios untouched — the guiding rule). The slot is safe by
// construction: a single bare right-click on the interior is a deliberate
// no-op, so a missed double costs nothing.

async function doubleRightClick(window: any, x: number, y: number) {
  const m = window.mouse;
  await m.move(x, y);
  await m.down({ button: 'right' });
  await m.up({ button: 'right' });
  await m.down({ button: 'right' });
  await m.up({ button: 'right' });
}

test('double right-click zooms a pane and back, restoring the layout exactly', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('localdb');
  await gw.splitFocusedPaneVertical();
  const before = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  expect(before).toHaveLength(2);
  const left = before[0];

  // Zoom the LEFT pane: it owns the whole layout, the right pane vanishes.
  await doubleRightClick(window, left.x + left.w / 2, left.y + left.h / 2);
  await gw.waitIdle();
  const zoomed = await gw.panes();
  expect(zoomed, 'only the zoomed pane is laid out').toHaveLength(1);
  expect(zoomed[0].id).toBe(left.id);
  expect(zoomed[0].w, 'zoomed pane spans the full width').toBeGreaterThan(left.w * 1.5);

  // Unzoom: byte-identical layout.
  await doubleRightClick(window, zoomed[0].x + zoomed[0].w / 2, zoomed[0].y + zoomed[0].h / 2);
  await gw.waitIdle();
  const after = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  expect(after).toHaveLength(2);
  for (let i = 0; i < 2; i++) {
    expect(after[i].id).toBe(before[i].id);
    expect(after[i].x).toBeCloseTo(before[i].x, 3);
    expect(after[i].w).toBeCloseTo(before[i].w, 3);
  }

  // Two SLOW right-clicks must NOT zoom (the safety property of the slot).
  const m = window.mouse;
  await m.move(left.x + left.w / 2, left.y + left.h / 2);
  await m.down({ button: 'right' });
  await m.up({ button: 'right' });
  await window.waitForTimeout(600);
  await m.down({ button: 'right' });
  await m.up({ button: 'right' });
  await gw.waitIdle();
  expect(await gw.panes(), 'slow double must stay unzoomed').toHaveLength(2);
});
