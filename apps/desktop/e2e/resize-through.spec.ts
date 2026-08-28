import { test, expect } from './fixtures';

// Issue #79: dragging a divider cascades tmux-style — the adjacent pane
// compresses to its 32px minimum first, then the drag starts compressing the
// NEXT pane along the axis. The old behavior clamped the drag as soon as the
// whole opposite side hit 32px combined, squashing its panes proportionally.

test('a border drag compresses the middle pane to its min, then the third; backing off un-reds', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('home');

  // Three columns: split twice (each split halves the focused pane).
  await gw.splitFocusedPaneVertical();
  await gw.splitFocusedPaneVertical();
  const before = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  expect(before).toHaveLength(3);
  const [p1, p2, p3] = before;

  // Drag the FIRST divider (right edge of pane 1) rightward, far enough to
  // consume all of pane 2's slack and bite into pane 3 — assert the cascade
  // MID-DRAG (releasing there would close the crushed pane, issue #217).
  const gx = p1.x + p1.w;
  const gy = p1.y + p1.h / 2;
  const travel = p2.w - 32 + 60; // p2's full slack, plus 60px into p3
  await window.mouse.move(gx - 2, gy);
  await window.mouse.down();
  await window.mouse.move(gx + travel, gy, { steps: 8 });
  const mid = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  const [m1, m2, m3] = mid;
  expect(m2.w, 'middle pane sits at its minimum').toBeLessThan(40);
  expect(m1.w, 'the drag traveled past the middle pane into the third').toBeGreaterThan(
    p1.w + (p2.w - 40),
  );
  expect(m3.w, 'the third pane absorbed the overflow').toBeLessThan(p3.w - 40);
  for (const m of mid) {
    expect(m.w).toBeGreaterThanOrEqual(31.5);
  }

  // Back off to within pane 2's slack and release: pressure released, the
  // crushed pane un-reds — NOTHING closes (issue #217).
  await window.mouse.move(gx + 60, gy, { steps: 8 });
  await window.mouse.up();
  await gw.waitIdle();
  const after = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  expect(after, 'backing off before release closes nothing').toHaveLength(3);
  expect(after[0].w, 'the resize applied at the release point').toBeCloseTo(p1.w + 60, 0);
});

// Issue #112 (property carried to the LEFT button by #203): a border drag
// must move only the grabbed border — the old single-ratio write visibly
// slid nested borders the user never touched.
test('a left-button border drag moves only the grabbed border', async ({ gw }) => {
  await gw.enterPlugin('home');
  await gw.splitFocusedPaneVertical();
  await gw.splitFocusedPaneVertical();
  const before = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  const [p1, p2, p3] = before;

  // Left-drag the FIRST divider right by 60px — well within pane 2's slack.
  const gx = p1.x + p1.w;
  const gy = p1.y + p1.h / 2;
  await gw.leftDragScreen(gx - 2, gy, gx + 60, gy);

  const after = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  const [q1, q2, q3] = after;
  expect(q1.w, 'grabbed border moved').toBeCloseTo(p1.w + 60, 0);
  expect(q2.w, 'adjacent pane absorbed it all').toBeCloseTo(p2.w - 60, 0);
  expect(q3.w, 'the untouched border did not move').toBeCloseTo(p3.w, 0);
});

test('a left-button drag far past the wall collapses on release (#203)', async ({ gw }) => {
  await gw.enterPlugin('home');
  await gw.splitFocusedPaneVertical();
  const before = await gw.panes();
  expect(before).toHaveLength(2);
  const left = before.slice().sort((a, b) => a.x - b.x)[0];

  // Drag the divider almost to the left edge: the applied cursor puts the
  // left side under the close threshold → release collapses it.
  const gx = left.x + left.w;
  await gw.leftDragScreen(gx - 2, left.y + left.h / 2, left.x + 4, left.y + left.h / 2);
  await expect.poll(async () => (await gw.panes()).length).toBe(1);
});

// The middle-pane close (issue #217, superseding #204's corridor-edge
// band): pressure builds at each bump. Dragging a MIDDLE pane's border all
// the way across it — past the point where it reached its minimum — reds
// it, and release closes exactly it: no traveling to the screen edge.
test('crushing a middle pane past its bump closes it on release (#217)', async ({
  gw,
}) => {
  await gw.enterPlugin('home');
  await gw.splitFocusedPaneVertical();
  await gw.splitFocusedPaneVertical();
  const before = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  expect(before).toHaveLength(3);
  const [p1, p2] = before;

  // Grab the NESTED divider (between panes 2 and 3) and drag LEFT through
  // pane 2's slack and 60px past its bump into pane 1's territory: pane 2
  // is pressed to close; pane 1 (still far from its own bump) is not.
  const gx = p2.x + p2.w;
  const gy = p2.y + p2.h / 2;
  const releaseX = p2.x - 60;
  await gw.leftDragScreen(gx - 2, gy, releaseX, gy);

  await expect.poll(async () => (await gw.panes()).length).toBe(2);
  const after = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  expect(after.map((q) => q.id)).not.toContain(p2.id);
  expect(after[0].id, 'the first pane survives untouched').toBe(p1.id);
});

// The one-click close class (#204): a bare divider click must never close.
// Under the crush model (#217) the guard is the strict pressed-past-the-
// bump compare — a click's cursor sits AT the boundary, past no bump, even
// when the neighbor already rests at its minimum.
test('a bare click on a divider closes nothing (#204)', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  await gw.splitFocusedPaneVertical();
  const before = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  expect(before).toHaveLength(2);
  const [left] = before;
  await window.mouse.click(left.x + left.w - 2, left.y + left.h / 2);
  await gw.waitIdle();
  expect((await gw.panes()).length, 'a click can never close a pane').toBe(2);
});

test('pressing just past the bump closes on release — no travel to the edge needed (#217)', async ({
  gw,
}) => {
  await gw.enterPlugin('home');
  await gw.splitFocusedPaneVertical();
  const before = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  expect(before).toHaveLength(2);
  const [left] = before;
  const gy = left.y + left.h / 2;
  // The left pane's bump is at left.x+32 (its minimum). Releasing at +20 is
  // pressed past it — under #204's model this resized; pressure closes now.
  await gw.leftDragScreen(left.x + left.w - 2, gy, left.x + 20, gy);
  await expect.poll(async () => (await gw.panes()).length).toBe(1);
});

// Requirement (#217): which SIDE of a border you grab must not matter. The
// B-side band (2px right of the divider) resolves the same divider and
// produces the same resize as the A-side grab every other test uses.
test('grabbing the border from its far side behaves identically (#217)', async ({ gw }) => {
  await gw.enterPlugin('home');
  await gw.splitFocusedPaneVertical();
  await gw.splitFocusedPaneVertical();
  const before = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  const [p1, p2, p3] = before;
  const gx = p1.x + p1.w;
  const gy = p1.y + p1.h / 2;
  // Grab 2px RIGHT of the divider (inside pane 2's band) and drag right.
  await gw.leftDragScreen(gx + 2, gy, gx + 60, gy);
  const after = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  expect(after[0].w, 'grabbed border moved').toBeCloseTo(p1.w + 60, 0);
  expect(after[1].w, 'adjacent pane absorbed it all').toBeCloseTo(p2.w - 60, 0);
  expect(after[2].w, 'the untouched border did not move').toBeCloseTo(p3.w, 0);
});

// "More and more panes go red until I let go — they all close" (#217):
// pressing the nested divider past BOTH bumps on its side closes both.
test('pressing past two bumps closes both panes on release (#217)', async ({ gw }) => {
  await gw.enterPlugin('home');
  await gw.splitFocusedPaneVertical();
  await gw.splitFocusedPaneVertical();
  const before = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  expect(before).toHaveLength(3);
  const [p1, p2, p3] = before;

  // Grab the nested divider (right edge of pane 2) and drag to 4px from the
  // screen's left edge: past pane 2's bump AND pane 1's.
  const gx = p2.x + p2.w;
  const gy = p2.y + p2.h / 2;
  await gw.leftDragScreen(gx - 2, gy, p1.x + 4, gy);
  await expect.poll(async () => (await gw.panes()).length).toBe(1);
  const [survivor] = await gw.panes();
  expect(survivor.id, 'the pressed-into pane survives').toBe(p3.id);
});

// The #238 fix: crush deep through BOTH panes on a side, back off a hair
// past the wall, release — everything survives at its minimum. Under the
// old grab-size bump model the adjacent pane stayed red until the cursor
// retreated almost to the grab point, regrowing it far past its min.
test('backing off just past the wall after a deep crush closes nothing (#238)', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('home');
  await gw.splitFocusedPaneVertical();
  await gw.splitFocusedPaneVertical();
  const before = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  expect(before).toHaveLength(3);
  const [p1, , p3] = before;
  const gx = p1.x + p1.w;
  const gy = p1.y + p1.h / 2;
  const rightEdge = p3.x + p3.w;

  // Deep press: panes 2 and 3 both crush to min and go red.
  await window.mouse.move(gx - 2, gy);
  await window.mouse.down();
  await window.mouse.move(rightEdge - 10, gy, { steps: 8 });
  // Back off to just past the wall (both mins plus a little) and release.
  await window.mouse.move(rightEdge - 64 - 12, gy, { steps: 4 });
  await window.mouse.up();
  await gw.waitIdle();

  const after = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  expect(after, 'nothing closes').toHaveLength(3);
  expect(after[1].w, 'middle pane sits near its min').toBeLessThan(50);
  expect(after[2].w, 'third pane sits near its min').toBeLessThan(50);
});

test('the same divider closes either side by drag direction (#204)', async ({ gw }) => {
  await gw.enterPlugin('home');
  await gw.splitFocusedPaneVertical();
  const before = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  expect(before).toHaveLength(2);
  const [left, right] = before;
  const gy = left.y + left.h / 2;
  // Drag the divider RIGHT, all the way across the right pane to its far
  // edge: the RIGHT side closes.
  await gw.leftDragScreen(left.x + left.w - 2, gy, right.x + right.w - 4, gy);
  await expect.poll(async () => (await gw.panes()).length).toBe(1);
  const [survivor] = await gw.panes();
  expect(survivor.w, 'the left pane survived and owns the width').toBeGreaterThan(left.w);
});
