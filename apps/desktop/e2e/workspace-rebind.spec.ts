import { test, expect } from './fixtures';
import { tileAt } from './oracle';
import type { GridwellDriver } from './driver';

// seedTile creates a markdown tile at (cx, cy), types seed into it, ascends,
// and waits for the content to persist. Pane must be at grid level.
async function seedTile(
  gw: GridwellDriver,
  gridID: string,
  cx: number,
  cy: number,
  seed: string,
): Promise<string> {
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const created = tileAt(await gw.getGrid(gridID), 'text', cx, cy)!;
  expect(created, `markdown tile created at (${cx},${cy})`).toBeTruthy();
  await gw.descendCell(cx, cy);
  await gw.typeText(seed);
  await gw.ascendViaCrumb();
  await expect
    .poll(async () => gw.getTileContent(created.id), { timeout: 10_000 })
    .toBe(seed);
  return created.id;
}

// The 2026-07-18 incident's own shape: a workspace whose layout blob holds a
// text-descended leaf. Re-entering the workspace restores that descent, but
// nothing rebound the textarea singleton — it still holds the LAST tile the
// user edited (outside the workspace). The restored pane then displays the
// wrong document, and the next flush over it (the workspace bar ascent)
// persists those foreign bytes as the leaf tile's content.
async function workspaceState(window: any): Promise<{ depth: number; tileID?: string }> {
  return window.evaluate(() => (window as any).__gridwellTest.workspace());
}

test('re-entering a workspace rebinds the editor; leaving it never saves a foreign buffer', async ({ gw, window }) => {
  await gw.enterPlugin('local');
  const f = await gw.focused();
  const rootGrid = f.gridID;
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  // "Today" (the workspace's document), "Routine" (the outside document),
  // and the workspace pane tile.
  const todayID = await seedTile(gw, rootGrid, cx - 1, cy, 'today today');
  const routineID = await seedTile(gw, rootGrid, cx, cy + 1, 'routine routine');
  await gw.openPalette();
  await gw.dragCreate('pane', cx + 1, cy);
  const pt = tileAt(await gw.getGrid(rootGrid), 'pane', cx + 1, cy)!;
  expect(pt, 'pane tile created').toBeTruthy();

  // Enter the workspace ("organize this grid" default) and descend into
  // Today, so the layout blob records a text-descended leaf.
  await gw.descendCell(cx + 1, cy);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(1);
  await gw.descendCell(cx - 1, cy);
  await expect
    .poll(async () => gw.textareaValue(), { timeout: 10_000 })
    .toBe('today today');

  // Leave the workspace (bar ascent flushes + persists the layout with the
  // descent), then edit Routine OUTSIDE it — the singleton is now bound to
  // Routine.
  await gw.leaveWorkspace();
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(0);
  await expect
    .poll(async () => {
      try {
        return (await gw.getTileContent(pt.id)).includes('"text_focus"');
      } catch {
        return false;
      }
    }, { message: 'the layout blob must record the text descent', timeout: 10_000 })
    .toBe(true);
  await gw.descendCell(cx, cy + 1);
  await expect
    .poll(async () => gw.textareaValue(), { timeout: 10_000 })
    .toBe('routine routine');
  await gw.ascendViaCrumb(); // ascend out of Routine

  // Re-enter the workspace: the restored leaf is descended into Today and
  // MUST show Today's words, not the singleton's leftover Routine buffer.
  await gw.descendCell(cx + 1, cy);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(1);
  await expect
    .poll(async () => gw.textareaValue(), {
      message: 'the restored descent must display its own document',
      timeout: 10_000,
    })
    .toBe('today today');

  // Leave the workspace again — the boundary flush must not write anything
  // foreign into Today.
  const bar2 = await window.evaluate(() => (window as any).__gridwellTest.bar());
  const ws2 = bar2.segments.find((s: any) => s.kind === 'pane');
  await window.mouse.click(ws2.x + 20, bar2.top + bar2.height / 2, { button: 'right' });
  await gw.waitIdle();
  await expect
    .poll(async () => gw.getTileContent(todayID), { timeout: 10_000 })
    .toBe('today today');
  expect(await gw.getTileContent(routineID)).toBe('routine routine');
});
