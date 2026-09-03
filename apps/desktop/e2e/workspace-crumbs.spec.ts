import { test, expect } from './fixtures';
import { tileAt } from './oracle';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';

// The bar's chain inside a workspace, across the layout-blob seam.
//
// A workspace's durable place is its layout blob, and the bar's chain is that
// place projected. A pane that crossed into a plugin has frames below the
// crossing — the grid it started in, the doorway it came through — and those
// are the only way back out. Leaving and re-entering the workspace round-trips
// the whole stack through the blob, so this spec is written on what the bar
// shows after the re-entry, not on the encoder alone.

const FS_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'gridwell-crumbs-'));
test.use({ extraPlugins: [{ kind: 'fs', name: 'files', config: { root: FS_ROOT } }] });

// chainAfterBoundary returns the focused pane's own crumbs: everything after
// the innermost pane-tile boundary. The crumbs before it belong to the window
// — the root close-all crumb and one per open level.
function chainAfterBoundary(bar: { segments: any[] }): any[] {
  let last = -1;
  bar.segments.forEach((s, i) => {
    if (s.kind === 'pane') last = i;
  });
  return bar.segments.slice(last + 1);
}

test('a workspace re-entered inside a plugin keeps every crumb down from its root', async ({
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
  const pt = tileAt(await gw.getGrid(rootGrid), 'pane', wx, wy);
  expect(pt).toBeTruthy();

  // Inside the workspace, cross into a plugin: the pane's place is now the
  // home grid, then the plugin's root through the menu swatch.
  await gw.descendCell(wx, wy);
  await gw.clickPluginSwatch('files');
  const inside = await gw.focused();
  const pluginGrid = inside.gridID;
  expect(pluginGrid, 'the pane crossed into the plugin').not.toBe(rootGrid);

  const before = chainAfterBoundary(await gw.bar());
  expect(before.map((s) => s.anchor), 'live chain: the root grid, then the plugin').toEqual([
    rootGrid,
    pluginGrid,
  ]);

  // Leave: the ascent flushes the layout, and the write lands a beat later.
  await gw.leaveWorkspace();
  await expect
    .poll(async () => {
      try {
        return (await gw.getTileContent(pt!.id)).includes('"pane"');
      } catch {
        return false;
      }
    }, { message: 'the layout must be persisted before the re-entry', timeout: 10_000 })
    .toBe(true);

  // Re-enter: the chain must be what it was, root crumb included. Without it
  // there is no way back out of the plugin inside this workspace.
  await gw.descendCell(wx, wy);
  await expect
    .poll(async () => chainAfterBoundary(await gw.bar()).map((s) => s.anchor), {
      message: 'the restored chain must still start at the root grid',
      timeout: 15_000,
    })
    .toEqual([rootGrid, pluginGrid]);

  // And the root crumb still works: clicking it ascends out of the plugin,
  // inside the workspace.
  const bar = await gw.bar();
  const root = chainAfterBoundary(bar)[0];
  await window.mouse.click(root.x + root.w / 2, bar.top + bar.height / 2);
  await gw.waitIdle();
  await expect.poll(async () => (await gw.focused()).gridID).toBe(rootGrid);
  const depth = await window.evaluate(() => (window as any).__gridwellTest.workspace().depth);
  expect(depth, 'still inside the workspace').toBe(1);
});
