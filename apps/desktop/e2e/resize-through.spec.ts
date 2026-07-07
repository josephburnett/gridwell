import { test, expect } from './fixtures';

// Issue #79: dragging a divider cascades tmux-style — the adjacent pane
// compresses to its 32px minimum first, then the drag starts compressing the
// NEXT pane along the axis. The old behavior clamped the drag as soon as the
// whole opposite side hit 32px combined, squashing its panes proportionally.

test('a border drag compresses the middle pane to its min, then the third', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('localdb');

  // Three columns: split twice (each split halves the focused pane).
  await gw.splitFocusedPaneVertical();
  await gw.splitFocusedPaneVertical();
  const before = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  expect(before).toHaveLength(3);
  const [p1, p2, p3] = before;

  // Drag the FIRST divider (right edge of pane 1) rightward, far enough to
  // consume all of pane 2's slack and bite into pane 3.
  const gx = p1.x + p1.w;
  const gy = p1.y + p1.h / 2;
  const travel = p2.w - 32 + 60; // p2's full slack, plus 60px into p3
  await gw.leftDragScreen(gx - 2, gy, gx + travel, gy);

  const after = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  const [q1, q2, q3] = after;
  expect(q2.w, 'middle pane sits at its minimum').toBeLessThan(40);
  expect(q1.w, 'the drag traveled past the middle pane into the third').toBeGreaterThan(
    p1.w + (p2.w - 40),
  );
  expect(q3.w, 'the third pane absorbed the overflow').toBeLessThan(p3.w - 40);

  // The wall: no pane below its minimum.
  for (const q of after) {
    expect(q.w).toBeGreaterThanOrEqual(31.5);
  }
});

// Issue #112: the RIGHT-drag resize must also move only the grabbed border —
// it used to write one proportional ratio, visibly sliding nested borders the
// user never touched — while keeping its crush-to-collapse release semantics.
test('a right-button border drag moves only the grabbed border', async ({ gw }) => {
  await gw.enterPlugin('localdb');
  await gw.splitFocusedPaneVertical();
  await gw.splitFocusedPaneVertical();
  const before = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  const [p1, p2, p3] = before;

  // Right-drag the FIRST divider right by 60px — well within pane 2's slack.
  const gx = p1.x + p1.w;
  const gy = p1.y + p1.h / 2;
  await gw.rightDragScreen(gx - 2, gy, gx + 60, gy);

  const after = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  const [q1, q2, q3] = after;
  expect(q1.w, 'grabbed border moved').toBeCloseTo(p1.w + 60, 0);
  expect(q2.w, 'adjacent pane absorbed it all').toBeCloseTo(p2.w - 60, 0);
  expect(q3.w, 'the untouched border did not move').toBeCloseTo(p3.w, 0);
});

test('a right-button drag far past the wall still collapses on release', async ({ gw }) => {
  await gw.enterPlugin('localdb');
  await gw.splitFocusedPaneVertical();
  const before = await gw.panes();
  expect(before).toHaveLength(2);
  const left = before.slice().sort((a, b) => a.x - b.x)[0];

  // Drag the divider almost to the left edge: the raw cursor puts the left
  // side under the close threshold → release collapses it.
  const gx = left.x + left.w;
  await gw.rightDragScreen(gx - 2, left.y + left.h / 2, left.x + 4, left.y + left.h / 2);
  await expect.poll(async () => (await gw.panes()).length).toBe(1);
});
