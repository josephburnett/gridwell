import { test, expect } from './fixtures';

// A left press at the corner of three panes grabs a divider on BOTH axes and
// the one drag moves both. It is not a gesture of its own: pane.GrabDividers
// returns at most one divider per axis — the two meeting at a T-intersection
// belong to different tree nodes — and the arm path builds one ordinary
// resize per grabbed axis, so the minimum clamp, the cascade and the crush
// verdict are the same code on each. This spec crosses the seam the unit test
// cannot: the wasm shim's arm, the live drag, and the resulting layout.
//
// The corner belongs to the pane you press inside. In a T, the pane on the
// stem's far side touches only the one divider, which is why the press here is
// a few px inside the pane whose corner it actually is.

// tPanes builds the T: a vertical split, then a horizontal split of the left
// half. Returns the three panes as {lt, lb, right} plus the corner point.
async function tPanes(gw: any) {
  const byX = (await gw.panes()).slice().sort((a: any, b: any) => a.x - b.x);
  if (byX.length !== 3) throw new Error(`the T needs three panes, got ${byX.length}`);
  const right = byX[2];
  const [lt, lb] = byX.slice(0, 2).sort((a: any, b: any) => a.y - b.y);
  return { lt, lb, right, cornerX: lt.x + lt.w, cornerY: lt.y + lt.h };
}

test('a press at a three-pane corner arms both axes and one drag moves both splits', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('home');
  await gw.splitFocusedPaneVertical();
  let byX = (await gw.panes()).slice().sort((a: any, b: any) => a.x - b.x);
  await gw.focusPane(byX[0]);
  await gw.splitFocusedPaneHorizontal();
  expect((await gw.panes()).length, 'the T needs three panes').toBe(3);

  const t0 = await tPanes(gw);

  // Press 3px inside the corner-owning pane: within the 10px grab band of both
  // its right edge (the vertical divider) and its bottom edge (the horizontal
  // one).
  const px = t0.cornerX - 3;
  const py = t0.cornerY - 3;
  await window.mouse.move(px, py);
  await window.mouse.down();
  expect(
    await window.evaluate(() => (window as any).__gridwellTest.leftResizeAxes()),
    'a corner press grabs one divider per axis',
  ).toBe(2);

  // Drag diagonally: left moves the vertical split, up moves the horizontal
  // one. Each boundary lands on the release cursor's own axis coordinate.
  const toX = px - 80;
  const toY = py - 60;
  await window.mouse.move(toX, toY, { steps: 10 });
  await window.mouse.up();
  await gw.waitIdle();

  expect((await gw.panes()).length, 'a resize closes nothing').toBe(3);
  const t1 = await tPanes(gw);
  expect(t1.cornerX, 'the vertical split landed on the release x').toBeCloseTo(toX, 0);
  expect(t1.cornerY, 'the horizontal split landed on the release y').toBeCloseTo(toY, 0);
  expect(t1.lb.w, 'both left panes follow the vertical split').toBeCloseTo(t1.lt.w, 0);
  expect(t1.right.w, 'the right pane absorbed the horizontal travel').toBeCloseTo(
    t0.right.w + (t0.cornerX - toX),
    0,
  );
  expect(t1.lb.h, 'the bottom-left pane absorbed the vertical travel').toBeCloseTo(
    t0.lb.h + (t0.cornerY - toY),
    0,
  );
  expect(t1.right.h, 'the right pane spans both, so its height is untouched').toBeCloseTo(
    t0.right.h,
    0,
  );
});

test('a press on a divider away from the corner arms one axis and moves only it', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('home');
  await gw.splitFocusedPaneVertical();
  let byX = (await gw.panes()).slice().sort((a: any, b: any) => a.x - b.x);
  await gw.focusPane(byX[0]);
  await gw.splitFocusedPaneHorizontal();
  const t0 = await tPanes(gw);

  // Mid-height of the top-left pane: far from its bottom edge, so only the
  // vertical divider on its right edge is in the band.
  const px = t0.cornerX - 3;
  const py = t0.lt.y + t0.lt.h / 2;
  await window.mouse.move(px, py);
  await window.mouse.down();
  expect(
    await window.evaluate(() => (window as any).__gridwellTest.leftResizeAxes()),
    'a mid-divider press grabs one axis only',
  ).toBe(1);

  // The same diagonal drag: only the vertical split may move.
  const toX = px - 80;
  await window.mouse.move(toX, py - 60, { steps: 10 });
  await window.mouse.up();
  await gw.waitIdle();

  const t1 = await tPanes(gw);
  expect(t1.cornerX, 'the grabbed vertical split landed on the release x').toBeCloseTo(toX, 0);
  expect(t1.lt.h, 'the ungrabbed horizontal split did not move').toBeCloseTo(t0.lt.h, 0);
  expect(t1.lb.h, 'nor did its other side').toBeCloseTo(t0.lb.h, 0);
});
