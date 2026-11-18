import type { Page } from '@playwright/test';
import { test, expect } from './fixtures';
import { tileAt, GridSnapshot } from './oracle';
import type { GridwellDriver } from './driver';

// A gesture owns the pointer from its press to its own button's release: a
// press of the other button while it is in flight does nothing, and that
// button's release finishes nothing (gesture.Press, gesture.Release). Each
// test runs the first gesture to where it would have ended alone and checks
// it ended there, with nothing else written and the page still running.

function texts(snap: GridSnapshot): number {
  return (snap.tiles ?? []).filter((t) => t.kind === 'text').length;
}

async function textTile(gw: GridwellDriver, cx: number, cy: number) {
  const f = await gw.focused();
  await gw.openMenu();
  await gw.dragCreate('markdown', cx, cy);
  const t = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;
  expect(t, 'created text tile').toBeTruthy();
  await expect.poll(async () => (await gw.focused()).tileIds.includes(t.id)).toBe(true);
  return t;
}

// A Go panic ends the wasm, and every testhook call throws after it.
async function expectRunning(win: Page) {
  const detail = await win.evaluate(() => (window as any).__gridwellTest.idleDetail());
  expect(detail, 'the page still answers, with nothing armed').toEqual(
    expect.objectContaining({ dragging: false, leftResize: false, rightDrag: false }),
  );
}

test('a right press and release on a tile during a left-drag moves only the dragged tile', async ({ gw, window }) => {
  await gw.enterSource('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  const a = await textTile(gw, cx, cy);
  const b = await textTile(gw, cx, cy + 2);
  const n = texts(await gw.getGrid(f.gridID));

  const from = await gw.cellCenter(f.id, cx, cy);
  const overB = await gw.cellCenter(f.id, cx, cy + 2);
  const mid = await gw.cellCenter(f.id, cx - 2, cy + 2);
  const to = await gw.cellCenter(f.id, cx - 2, cy - 2);
  const m = window.mouse;
  await m.move(from.x, from.y);
  await m.down();
  await m.move(from.x + 8, from.y + 8);
  await m.move(overB.x, overB.y, { steps: 6 });
  await m.down({ button: 'right' });
  await m.move(mid.x, mid.y, { steps: 6 });
  await m.up({ button: 'right' });
  await m.move(to.x, to.y, { steps: 6 });
  await m.up();
  await gw.waitIdle();
  await expectRunning(window);

  const snap = await gw.getGrid(f.gridID);
  expect(tileAt(snap, 'text', cx - 2, cy - 2)?.id, 'the left-drag landed where the left button came up').toBe(a.id);
  expect(tileAt(snap, 'text', cx, cy + 2)?.id, 'the tile the right button pressed stayed').toBe(b.id);
  expect(tileAt(snap, 'text', cx - 2, cy + 2), 'the right release made nothing').toBeFalsy();
  expect(texts(snap), 'no tile was made').toBe(n);
});

for (const over of ['grid', 'bar'] as const) {
  test(`a left press during a right-drag copy leaves the copy to the right release, over the ${over}`, async ({ gw, window }) => {
    await gw.enterSource('home');
    const f = await gw.focused();
    const cx = Math.round(f.cx);
    const cy = Math.round(f.cy);
    const a = await textTile(gw, cx, cy);
    const n = texts(await gw.getGrid(f.gridID));

    const from = await gw.cellCenter(f.id, cx, cy);
    const empty = await gw.cellCenter(f.id, cx + 2, cy);
    const dest = await gw.cellCenter(f.id, cx - 2, cy);
    const bar = await gw.bar();
    const to = over === 'grid' ? dest : { x: bar.left + bar.width / 2, y: bar.top + bar.height / 2 };
    const m = window.mouse;
    await m.move(from.x, from.y);
    await m.down({ button: 'right' });
    await m.move(from.x + 8, from.y + 8);
    await m.move(empty.x, empty.y, { steps: 6 });
    // On empty grid a left press alone arms a pan.
    await m.down();
    await m.move(empty.x + 30, empty.y + 30, { steps: 4 });
    await m.move(to.x, to.y, { steps: 6 });
    await m.up({ button: 'right' });
    await m.up();
    await gw.waitIdle();
    await expectRunning(window);

    const snap = await gw.getGrid(f.gridID);
    expect(tileAt(snap, 'text', cx, cy)?.id, 'the source stayed').toBe(a.id);
    if (over === 'grid') {
      const copy = tileAt(snap, 'text', cx - 2, cy);
      expect(copy, 'the copy landed where the right button came up').toBeTruthy();
      expect(copy!.id).not.toBe(a.id);
      expect(texts(snap), 'one copy').toBe(n + 1);
    } else {
      expect(texts(snap), 'a copy released over the bar makes nothing').toBe(n);
    }
  });
}
