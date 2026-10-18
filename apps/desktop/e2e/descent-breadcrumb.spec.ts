import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// The bottom bar shows the focused pane's descent chain, root included, and a
// left-click on a crumb ascends all the way back to that level. A three-deep
// descent then one click on the root crumb: every frame pops, the root lands on
// the viewport the user left it at, and the intermediate framing is persisted
// so a re-descent restores the place the jump left.

async function bar(window: any): Promise<{ top: number; height: number; segments: any[] }> {
  return window.evaluate(() => (window as any).__gridwellTest.bar());
}

async function chainSegs(window: any): Promise<any[]> {
  return (await bar(window)).segments.filter((s: any) => s.kind === 'chain');
}

async function clickChainCrumb(gw: any, window: any, index: number): Promise<void> {
  const b = await bar(window);
  const seg = b.segments.find((s: any) => s.kind === 'chain' && s.index === index);
  expect(seg, `chain crumb ${index} must be in the bar`).toBeTruthy();
  await window.mouse.click(seg.x + seg.w / 2, b.top + b.height / 2);
  await gw.waitIdle();
}

test('a chain crumb click ascends all the way to that level and keeps the round trip', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const f0 = await gw.focused();
  const rootGrid = f0.gridID;
  const cx = Math.round(f0.cx);
  const cy = Math.round(f0.cy);

  // At the root the chain is just the root crumb.
  let segs = await chainSegs(window);
  expect(segs.length).toBe(1);
  expect(segs[0].anchor, 'the root crumb names the anchor').toBe(f0.anchor);

  // Wheel-zoom over empty space, since over a door the wheel zooms the door.
  // This gives the root a viewport the jump's landing can be compared against.
  const rootEmpty = await gw.cellCenter(f0.id, cx + 5, cy + 3);
  await window.mouse.move(rootEmpty.x, rootEmpty.y);
  await window.mouse.wheel(0, 120);
  await gw.waitIdle();
  const before = await gw.focused();
  expect(before.zoom).toBeLessThan(1);

  // Door, door, markdown, descending each level.
  await gw.openMenu();
  await gw.dragCreate('door', cx, cy);
  const W1 = tileAt(await gw.getGrid(rootGrid), 'door', cx, cy)!;
  expect(W1).toBeTruthy();
  await gw.descendCell(cx, cy);
  await expect.poll(async () => (await gw.focused()).gridID).not.toBe(rootGrid);
  const f1 = await gw.focused();
  const g1 = f1.gridID;

  const c1x = Math.round(f1.cx);
  const c1y = Math.round(f1.cy);
  await gw.openMenu();
  await gw.dragCreate('door', c1x, c1y);
  const W2 = tileAt(await gw.getGrid(g1), 'door', c1x, c1y)!;
  expect(W2).toBeTruthy();
  await gw.descendCell(c1x, c1y);
  await expect.poll(async () => (await gw.focused()).gridID).not.toBe(g1);

  // W2's grid gets a viewport too, which the jump must write back as W2's
  // framing and a re-descent must restore.
  let f2 = await gw.focused();
  const g2 = f2.gridID;
  // The grid is empty, so the cell just right of center is safe; the
  // post-descent zoom keeps only about one cell either side on-screen.
  const g2Empty = await gw.cellCenter(f2.id, Math.round(f2.cx) + 1, Math.round(f2.cy));
  await window.mouse.move(g2Empty.x, g2Empty.y);
  await window.mouse.wheel(0, 120);
  await gw.waitIdle();
  f2 = await gw.focused();

  const c2x = Math.round(f2.cx);
  const c2y = Math.round(f2.cy);
  await gw.openMenu();
  await gw.dragCreate('markdown', c2x, c2y);
  const MD = tileAt(await gw.getGrid(g2), 'text', c2x, c2y)!;
  expect(MD).toBeTruthy();
  const beforeMd = await gw.focused(); // g2 viewport the jump must preserve
  await gw.descendCell(c2x, c2y);
  await expect.poll(async () => (await gw.focused()).textFocus).toBe(MD.id);

  // The chain names every level in order, root included.
  segs = await chainSegs(window);
  expect(segs.map((s: any) => s.tileID || 'root')).toEqual(['root', W1.id, W2.id, MD.id]);
  expect(segs[3].text, 'the leaf crumb is a text descent').toBe(true);
  expect((await gw.focused()).placeDepth, 'three doors deep').toBe(3);

  // One click on the root crumb: the intermediate pops are instant and the
  // final hop animates.
  await clickChainCrumb(gw, window, 0);
  await expect.poll(async () => (await gw.focused()).gridID, { timeout: 10_000 }).toBe(rootGrid);
  const landed = await gw.focused();
  expect(landed.textFocus).toBe('');
  expect(landed.path.length).toBe(0);
  expect(landed.zoom, 'the root viewport zoom survives the jump').toBeCloseTo(before.zoom, 5);
  expect(landed.cx).toBeCloseTo(before.cx, 5);
  expect(landed.cy).toBeCloseTo(before.cy, 5);
  expect(landed.placeDepth, 'every frame popped — none orphaned').toBe(0);
  expect((await chainSegs(window)).length).toBe(1);

  // Empty bar space owns no gesture, so a right-click there must not ascend.
  await gw.descendCell(cx, cy);
  await expect.poll(async () => (await gw.focused()).gridID).toBe(g1);
  const b2 = await bar(window);
  const chainEnd = b2.segments.filter((s: any) => s.kind === 'chain').pop()!;
  const emptyX = chainEnd.x + chainEnd.w + 20;
  await window.mouse.click(emptyX, b2.top + b2.height / 2, { button: 'right' });
  await gw.waitIdle();
  expect((await gw.focused()).gridID, 'empty-bar right-click must not ascend').toBe(g1);
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).gridID, { timeout: 10_000 }).toBe(rootGrid);
  await gw.waitIdle(); // the ascent animates; input is blocked until it settles

  // Re-descending lands W2's grid where the jump left it. Zoom round-trips
  // exactly; the center is quantized to the stored origin, so allow the
  // half-cell.
  await gw.descendCell(cx, cy);
  await expect.poll(async () => (await gw.focused()).gridID).toBe(g1);
  await gw.descendCell(c1x, c1y);
  await expect.poll(async () => (await gw.focused()).gridID).toBe(g2);
  const back = await gw.focused();
  expect(back.zoom).toBeCloseTo(beforeMd.zoom, 3);
  expect(Math.abs(back.cx - beforeMd.cx)).toBeLessThan(0.6);
  expect(Math.abs(back.cy - beforeMd.cy)).toBeLessThan(0.6);
});
