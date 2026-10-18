import { test, expect } from './fixtures';
import { tileAt, writeContent } from './oracle';

// Copy semantics for workspaces, driven through the real gesture: an in-grid
// right-drag copy shares the layout blob by content address, and the copies
// diverge on the first edit, so rearranging the copy can never touch the
// original's arrangement. Cross-plugin byte-copy semantics are pinned at the
// routing seam in internal/server crossplugin_copy_test.go; this is the
// gesture-to-store composition for the common case.


async function workspaceState(window: any): Promise<{ depth: number }> {
  return window.evaluate(() => (window as any).__gridwellTest.workspace());
}

async function barLeave(gw: any): Promise<void> {
  await gw.leaveWorkspace();
}

test('copying a workspace: shared blob, independent divergence', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const rootGrid = f.gridID;
  const ax = Math.round(f.cx);
  const ay = Math.round(f.cy);

  // A split makes the original's blob distinctive.
  await gw.openMenu();
  await gw.dragCreate('workspace', ax, ay);
  const orig = tileAt(await gw.getGrid(rootGrid), 'workspace', ax, ay);
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

  // The copy gesture: right-drag from the tile's center to a free cell.
  await gw.copyTileCell(ax, ay, ax, ay + 2);
  const copy = tileAt(await gw.getGrid(rootGrid), 'workspace', ax, ay + 2);
  expect(copy, 'copy landed').toBeTruthy();
  expect(copy!.id).not.toBe(orig!.id);
  expect(await gw.getTileContent(copy!.id), 'copy shares the layout bytes').toBe(origBlob);

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
  }, { message: "the copy's blob must diverge", timeout: 10_000 }).toBe(true);
  await barLeave(gw);

  expect(await gw.getTileContent(orig!.id), "editing the copy must never touch the original").toBe(origBlob);
});

// A corrupt, or newer-format, layout blob: the workspace opens read-only on a
// default pane, reports once, and never writes over the blob it could not read.
// Writing would let an older client rewrite a newer arrangement.
test('an unreadable layout opens read-only and is never overwritten', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const rootGrid = f.gridID;
  const ax = Math.round(f.cx);
  const ay = Math.round(f.cy);

  await gw.openMenu();
  await gw.dragCreate('workspace', ax, ay);
  const pt = tileAt(await gw.getGrid(rootGrid), 'workspace', ax, ay);
  expect(pt).toBeTruthy();

  // A future-format blob, written directly. The server treats layout bytes as
  // opaque; only the client's codec versions them.
  const futureBlob = JSON.stringify({ v: 99, root: { exotic: true } });
  await writeContent(gw.origin, pt!.id, 0, Buffer.from(futureBlob));

  // No echo-wait here, deliberately: the direct write races its own event echo,
  // and descending inside that window is what this pins. The level fetch
  // (client/nav/level.go) must refetch the tile rather than trust the cached
  // row, or a stale BlobID of 0 installs the writable default and the persister
  // overwrites the blob. It first showed up as a one-run flake.
  await gw.descendCell(ax, ay);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(1);
  expect(
    await window.evaluate(() => (window as any).__gridwellTest.workspace().readOnly),
    'an unreadable blob must open read-only',
  ).toBe(true);

  // Arrange anyway and give the persister time to run; the blob must not change.
  await gw.splitFocusedPaneVertical();
  await window.waitForTimeout(1_500); // two debounce windows
  expect(await gw.getTileContent(pt!.id), 'read-only session must never overwrite').toBe(futureBlob);
  await barLeave(gw);
});
