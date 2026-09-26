import { test, expect, homePassword, loginToken } from './fixtures';
import { tileAt, updateText, writeContent } from './oracle';
import { settle } from './cadence';
import { dumpViaDoor, TraceLine } from './trace';

// A grid's view has one owner, its framing row, and the pane-tile layout blob
// holds the arrangement only. These cross the seam from the gesture to the
// node's own record of what it wrote, and from stored bytes to what a restored
// leaf shows.

async function workspaceDepth(window: any): Promise<number> {
  return (await window.evaluate(() => (window as any).__gridwellTest.workspace())).depth;
}

function storeWrites(lines: TraceLine[], verb: (msg: string) => boolean): number {
  return lines.filter((l) => l.src === 'store' && l.kind === 'write' && verb(l.msg)).length;
}
const isLayout = (m: string) => m === 'SetPaneLayout';
const isFraming = (m: string) => m.startsWith('SetFraming');

// The scroll of whichever text overlay is showing, -1 when neither is.
async function shownScroll(window: any): Promise<number> {
  return window.evaluate(() => {
    for (const id of ['gw-text-editor', 'gw-rendered-view']) {
      const el = document.getElementById(id);
      if (el && el.style.display !== 'none') return el.scrollTop;
    }
    return -1;
  });
}

function rpcStarts(lines: TraceLine[], verb: string): number {
  return lines.filter((l) => l.src === 'router' && l.kind === 'rpc' && l.msg === `${verb} start`).length;
}

test('a pan inside a pane tile writes the framing row alone, and the round trip returns to it', async ({
  gw,
  home,
  window,
}) => {
  const token = await loginToken(gw.origin, homePassword(home));
  const c = await gw.cadences();
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const rootGrid = f.gridID;
  const wx = Math.round(f.cx);
  const wy = Math.round(f.cy);

  await gw.openPalette();
  await gw.dragCreate('pane', wx, wy);
  await gw.openPalette();
  await gw.dragCreate('well', wx + 1, wy);
  const pt = tileAt(await gw.getGrid(rootGrid), 'pane', wx, wy);
  const well = tileAt(await gw.getGrid(rootGrid), 'well', wx + 1, wy);
  expect(pt && well, 'pane tile and well persisted').toBeTruthy();

  await gw.descendCell(wx, wy);
  await expect.poll(() => workspaceDepth(window)).toBe(1);
  await gw.descendCell(wx + 1, wy);
  const childGrid = (await gw.focused()).gridID;
  expect(childGrid, 'descended into the well inside the pane tile').not.toBe(rootGrid);
  await expect
    .poll(async () => (await gw.getTileContent(pt!.id)).includes(well!.id), {
      message: 'the layout records the descent, which is arrangement',
      timeout: 10_000,
    })
    .toBe(true);
  await settle(window, Math.max(c.workspaceSaveMs, c.framingSaveMs));

  const before = await dumpViaDoor(gw.origin, token);
  const layoutBefore = storeWrites(before, isLayout);
  const writeContentBefore = rpcStarts(before, 'WriteContent');
  const framingBefore = storeWrites(before, isFraming);

  // The child is empty, so the pan press lands on no tile.
  await gw.wheelAtFocusedCenter(-300);
  const zc = await gw.focused();
  await gw.panFocusedGrid(Math.round(zc.cx), Math.round(zc.cy), Math.round(zc.cx) - 1, Math.round(zc.cy) - 1);
  const left = await gw.focused();

  await expect
    .poll(async () => storeWrites(await dumpViaDoor(gw.origin, token), isFraming), {
      message: 'the pan settles into SetFraming',
      timeout: 10_000,
    })
    .toBeGreaterThan(framingBefore);
  // Two more layout windows, so a layout write armed by the pan has had its
  // chance to land.
  await settle(window, c.workspaceSaveMs);
  const after = await dumpViaDoor(gw.origin, token);
  expect(storeWrites(after, isLayout), 'a pan wrote the layout blob').toBe(layoutBefore);
  expect(rpcStarts(after, 'WriteContent'), 'a pan sent WriteContent').toBe(writeContentBefore);

  // Out of the pane tile and back in: the blob is read again, and the leaf
  // shows the grid where the framing row says it was left.
  await gw.leaveWorkspace();
  await expect.poll(() => workspaceDepth(window)).toBe(0);
  await gw.descendCell(wx, wy);
  await expect.poll(() => workspaceDepth(window)).toBe(1);
  await expect.poll(async () => (await gw.focused()).gridID).toBe(childGrid);
  await expect.poll(async () => (await gw.focused()).zoom, { message: 'zoom round-tripped' })
    .toBeCloseTo(left.zoom, 1);
  const back = await gw.focused();
  expect(back.cx, 'center x round-tripped').toBeCloseTo(left.cx, 1);
  expect(back.cy, 'center y round-tripped').toBeCloseTo(left.cy, 1);

  const end = await dumpViaDoor(gw.origin, token);
  expect(storeWrites(end, isLayout), 'the round trip wrote the layout blob').toBe(layoutBefore);
  await gw.leaveWorkspace();
});

test("a stored blob's viewport is not read: a leaf restores to its grid's framing row", async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const rootGrid = f.gridID;
  const wx = Math.round(f.cx);
  const wy = Math.round(f.cy);

  await gw.openPalette();
  await gw.dragCreate('pane', wx, wy);
  await gw.openPalette();
  await gw.dragCreate('well', wx + 1, wy);
  const pt = tileAt(await gw.getGrid(rootGrid), 'pane', wx, wy);
  const well = tileAt(await gw.getGrid(rootGrid), 'well', wx + 1, wy);
  expect(pt && well, 'pane tile and well persisted').toBeTruthy();

  // Frame the well's grid outside any pane tile, so its row owns a view.
  await gw.descendCell(wx + 1, wy);
  await gw.wheelAtFocusedCenter(-300);
  const zc = await gw.focused();
  await gw.panFocusedGrid(Math.round(zc.cx), Math.round(zc.cy), Math.round(zc.cx) - 1, Math.round(zc.cy) - 1);
  const left = await gw.focused();
  await gw.middleClickCell(Math.round(left.cx), Math.round(left.cy));
  expect((await gw.focused()).gridID, 'ascended out of the child').toBe(rootGrid);
  await expect
    .poll(async () => Number(tileAt(await gw.getGrid(rootGrid), 'well', wx + 1, wy)?.viewZoom ?? 0), {
      message: 'the ascent wrote the well row',
    })
    .toBeGreaterThan(0);

  // A blob as every Gridwell before this one wrote it: the leaf on the well's
  // grid, with a viewport of its own that disagrees with the row.
  const blobCx = left.cx + 37;
  const blobCy = left.cy - 23;
  const old = JSON.stringify({
    v: 1,
    root: { pane: { id: 'p1', anchor: left.anchor, path: left.path, cx: blobCx, cy: blobCy, zoom: 2.5 } },
    focus: 'p1',
  });
  await writeContent(gw.origin, pt!.id, 0, Buffer.from(old));

  await gw.descendCell(wx, wy);
  await expect.poll(() => workspaceDepth(window)).toBe(1);
  await expect.poll(async () => (await gw.focused()).gridID).toBe(left.gridID);
  await expect
    .poll(async () => (await gw.focused()).cx, { message: "the leaf shows the row's center, not the blob's" })
    .toBeCloseTo(left.cx, 1);
  const shown = await gw.focused();
  expect(shown.cy).toBeCloseTo(left.cy, 1);
  expect(shown.zoom).toBeCloseTo(left.zoom, 1);
  expect(shown.cx).not.toBeCloseTo(blobCx, 0);
  await gw.leaveWorkspace();
});

test("a text leaf's scroll is its tile row's: scrolling writes SetTextView alone, and re-entry restores it", async ({
  gw,
  home,
  window,
}) => {
  const token = await loginToken(gw.origin, homePassword(home));
  const c = await gw.cadences();
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const rootGrid = f.gridID;
  const wx = Math.round(f.cx);
  const wy = Math.round(f.cy);

  await gw.openPalette();
  await gw.dragCreate('pane', wx, wy);
  await gw.openPalette();
  await gw.dragCreate('markdown', wx + 1, wy);
  const pt = tileAt(await gw.getGrid(rootGrid), 'pane', wx, wy)!;
  const doc = tileAt(await gw.getGrid(rootGrid), 'text', wx + 1, wy)!;
  await updateText(gw.origin, doc.id, Number(doc.version ?? 0), '# long\n\n' + 'line\n\n'.repeat(200));

  await gw.descendCell(wx, wy);
  await expect.poll(() => workspaceDepth(window)).toBe(1);
  await gw.descendCell(wx + 1, wy);
  await expect.poll(async () => (await gw.focused()).textFocus).toBe(doc.id);
  await expect
    .poll(async () => (await gw.getTileContent(pt.id)).includes(doc.id), { timeout: 10_000 })
    .toBe(true);
  await settle(window, Math.max(c.workspaceSaveMs, c.framingSaveMs));
  const layoutBefore = storeWrites(await dumpViaDoor(gw.origin, token), isLayout);

  // The mode is the row's too, so the leaf leaves in the one it did not open
  // in.
  const opened = (await gw.focused()).textMode;
  await gw.toggleTextMode();
  await expect.poll(async () => (await gw.focused()).textMode).not.toBe(opened);
  const mode = (await gw.focused()).textMode;
  const p = await gw.focused();
  await window.mouse.move(p.x + p.w / 2, p.y + p.h / 2);
  for (let i = 0; i < 8; i++) await window.mouse.wheel(0, 120);
  const textY = async () =>
    Number((tileAt(await gw.getGrid(rootGrid), 'text', wx + 1, wy) as { textY?: number | string })?.textY ?? 0);
  await expect.poll(textY, { message: 'the scroll reached the tile row', timeout: 10_000 }).toBeGreaterThan(0);
  await settle(window, c.workspaceSaveMs);
  expect(storeWrites(await dumpViaDoor(gw.origin, token), isLayout), 'a scroll wrote the layout blob')
    .toBe(layoutBefore);
  const scrolled = await textY();

  await gw.leaveWorkspace();
  await expect.poll(() => workspaceDepth(window)).toBe(0);
  await gw.descendCell(wx, wy);
  await expect.poll(() => workspaceDepth(window)).toBe(1);
  await expect.poll(async () => (await gw.focused()).textFocus).toBe(doc.id);
  await expect.poll(async () => (await gw.focused()).textMode, { message: "re-entry shows the row's mode" })
    .toBe(mode);
  await expect
    .poll(() => shownScroll(window), {
      message: "re-entry shows the row's scroll",
    })
    .toBeCloseTo(scrolled, -1);
  await gw.leaveWorkspace();
});
