import { test, expect } from './fixtures';
import { tileAt, writeContent } from './oracle';

// Clone semantics for workspaces, driven through the real gesture: an in-grid
// right-drag clone shares the layout blob by content address, and the copies
// diverge on the first edit, so rearranging the clone can never touch the
// original's arrangement. Cross-plugin byte-copy semantics are pinned at the
// routing seam in internal/server crossplugin_clone_test.go; this is the
// gesture-to-store composition for the common case.


async function workspaceState(window: any): Promise<{ depth: number }> {
  return window.evaluate(() => (window as any).__gridwellTest.workspace());
}

async function barLeave(gw: any): Promise<void> {
  // The bar lives inside the focused pane, and a crumb click goes to that crumb,
  // so leaving means clicking the crumb before the pane boundary.
  await gw.leaveWorkspace();
}

test('cloning a workspace: shared blob, independent divergence', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const rootGrid = f.gridID;
  const ax = Math.round(f.cx);
  const ay = Math.round(f.cy);

  // Create and arrange the original, with a split, so the blob is distinctive.
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

  // Diverge the clone: enter it and split again, for three panes.
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

  // The original is untouched, byte for byte.
  expect(await gw.getTileContent(orig!.id), "editing the clone must never touch the original").toBe(origBlob);
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

  await gw.openPalette();
  await gw.dragCreate('pane', ax, ay);
  const pt = tileAt(await gw.getGrid(rootGrid), 'pane', ax, ay);
  expect(pt).toBeTruthy();

  // A future-format blob, written directly. The server treats layout bytes as
  // opaque; only the client's codec versions them.
  const futureBlob = JSON.stringify({ v: 99, root: { exotic: true } });
  await writeContent(gw.origin, pt!.id, 0, Buffer.from(futureBlob));

  // No echo-wait here, deliberately: the direct write races its own event echo,
  // and descending inside that window is the trap this pins. descendLevel must
  // refetch the tile rather than trust the cached row, or a stale BlobID of 0
  // installs the writable default and the persister overwrites the blob. An
  // earlier version of this spec found that as a one-run flake.
  await gw.descendCell(ax, ay);
  await expect.poll(async () => (await workspaceState(window)).depth).toBe(1);
  expect(
    await window.evaluate(() => (window as any).__gridwellTest.workspace().readOnly),
    'an unreadable blob must open read-only',
  ).toBe(true);

  // Arrange anyway, with a split, and give the persister every chance to
  // misbehave: the blob must stay byte-identical.
  await gw.splitFocusedPaneVertical();
  await window.waitForTimeout(1_500); // two debounce windows
  expect(await gw.getTileContent(pt!.id), 'read-only session must never overwrite').toBe(futureBlob);
  await barLeave(gw);
});
