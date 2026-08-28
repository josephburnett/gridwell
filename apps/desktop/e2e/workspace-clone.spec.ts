import { test, expect } from './fixtures';
import { tileAt, writeContent } from './oracle';

// Clone semantics for workspaces, driven through the real gesture: an
// in-grid right-drag clone shares the layout blob by content address, and
// the copies DIVERGE on first edit — rearranging the clone can never touch
// the original's arrangement (the eager-copy face of the guiding rule).
// Cross-plugin byte-copy semantics are pinned at the routing seam
// (internal/server crossplugin_clone_test.go); this spec is the gesture-to-
// store composition for the common case.


async function workspaceState(window: any): Promise<{ depth: number }> {
  return window.evaluate(() => (window as any).__gridwellTest.workspace());
}

async function barLeave(gw: any): Promise<void> {
  // The bar lives inside the FOCUSED pane (issue #220); leaving is the
  // crumb BEFORE the pane boundary (one-chain nav, #245: click = go there).
  await gw.leaveWorkspace();
}

test('cloning a workspace: shared blob, independent divergence', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const rootGrid = f.gridID;
  const ax = Math.round(f.cx);
  const ay = Math.round(f.cy);

  // Create + arrange the original (a split, so the blob is distinctive).
  await gw.openPalette();
  await gw.dragCreate('pane', ax, ay);
  const orig = tileAt(await gw.getGrid(rootGrid), 'pane', ax, ay);
  expect(orig).toBeTruthy();
  await gw.descendCell(ax, ay);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(1);
  await gw.splitFocusedPaneVertical();
  await expect.poll(async () => {
    try {
      return (await gw.getTileContent(orig!.id)).includes('"split"');
    } catch {
      return false;
    }
  }, { timeout: 10_000 }).toBe(true);
  await barLeave(gw);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(0);
  const origBlob = await gw.getTileContent(orig!.id);

  // The clone gesture: right-drag from the tile's center to a free cell.
  await gw.cloneTileCell(ax, ay, ax, ay + 2);
  const copy = tileAt(await gw.getGrid(rootGrid), 'pane', ax, ay + 2);
  expect(copy, 'clone landed').toBeTruthy();
  expect(copy!.id).not.toBe(orig!.id);
  expect(await gw.getTileContent(copy!.id), 'clone shares the layout bytes').toBe(origBlob);

  // Diverge the CLONE: enter it and split again (3 panes).
  await gw.descendCell(ax, ay + 2);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(1);
  await expect.poll(async () => (await gw.panes()).length).toBe(2);
  await gw.splitFocusedPaneVertical();
  await expect.poll(async () => (await gw.panes()).length).toBe(3);
  await expect.poll(async () => {
    try {
      return (await gw.getTileContent(copy!.id)) !== origBlob;
    } catch {
      return false;
    }
  }, { message: "the clone's blob must diverge", timeout: 10_000 }).toBe(true);
  await barLeave(gw);

  // The ORIGINAL is untouched — byte-for-byte.
  expect(await gw.getTileContent(orig!.id), "editing the clone must never touch the original").toBe(origBlob);
});

// A corrupt (or newer-format) layout blob: the workspace opens READ-ONLY on
// a default pane, reports once, and — the load-bearing half — NEVER writes
// over the blob it could not read (a downgrade would rewrite history).
test('an unreadable layout opens read-only and is never overwritten', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const rootGrid = f.gridID;
  const ax = Math.round(f.cx);
  const ay = Math.round(f.cy);

  await gw.openPalette();
  await gw.dragCreate('pane', ax, ay);
  const pt = tileAt(await gw.getGrid(rootGrid), 'pane', ax, ay);
  expect(pt).toBeTruthy();

  // A future-format blob, written directly (the server treats layout bytes
  // as opaque — only the client's codec versions them).
  const futureBlob = JSON.stringify({ v: 99, root: { exotic: true } });
  await writeContent(gw.origin, pt!.id, 0, Buffer.from(futureBlob));

  // No echo-wait here, deliberately: the direct write races its own SSE
  // echo, and descending inside that window is exactly the trap this pins —
  // startWorkspaceDescent must REFETCH the tile rather than trust the
  // cached row, or a stale BlobID 0 would install the WRITABLE default and
  // the persister could overwrite the blob (found as a one-run flake of an
  // earlier version of this spec; the app now refetches).
  await gw.descendCell(ax, ay);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(1);
  expect(
    await window.evaluate(() => (window as any).__gridwellTest.workspace().readOnly),
    'an unreadable blob must open read-only',
  ).toBe(true);

  // Arrange anyway (a split) and give the persister every chance to
  // misbehave: the blob must stay byte-identical.
  await gw.splitFocusedPaneVertical();
  await window.waitForTimeout(1_500); // two debounce windows
  expect(await gw.getTileContent(pt!.id), 'read-only session must never overwrite').toBe(futureBlob);
  await barLeave(gw);
});
