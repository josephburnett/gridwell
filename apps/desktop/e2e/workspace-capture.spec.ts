import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// The first descent into a never-arranged pane tile captures the current window
// layout, so the user keeps looking at exactly what they had, now inside the
// workspace, and the capture persists as the tile's arrangement with no save
// gesture. A single-pane window captures a single pane, which is what the
// roundtrip and bar specs pin.

async function depth(window: any): Promise<number> {
  return window.evaluate(() => (window as any).__gridwellTest.workspace().depth);
}

test('first descent captures the current split; later descents restore it', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('home');
  await gw.splitFocusedPaneVertical();
  const panes0 = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  expect(panes0).toHaveLength(2);
  const rootGrid = panes0[0].gridID;

  // Drop the pane tile in the left pane and descend into it.
  await gw.focusPane(panes0[0]);
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  await gw.openPalette();
  await gw.dragCreate('pane', cx, cy);
  const pt = tileAt(await gw.getGrid(rootGrid), 'pane', cx, cy)!;
  expect(pt, 'pane tile created').toBeTruthy();
  await gw.descendCell(cx, cy);
  await expect.poll(async () => depth(window)).toBe(1);

  // The capture: both panes survive into the workspace, framing the same grid.
  const inner = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  expect(inner, 'the split was captured').toHaveLength(2);
  for (const p of inner) {
    expect(p.gridID, 'captured panes keep their grids').toBe(rootGrid);
  }

  // The capture persists with no gesture: the layout persister's first tick
  // writes it, since a never-arranged tile has a nil baseline.
  await expect
    .poll(
      async () => {
        try {
          return (await gw.getTileContent(pt.id)).includes('"split"') ? 'captured' : 'single';
        } catch {
          return '';
        }
      },
      { timeout: 10_000 },
    )
    .toBe('captured');

  // Leave, waiting for idle because the return animation swallows clicks, then
  // re-enter: an ordinary blob descent restores the captured split.
  await gw.leaveWorkspace();
  await expect.poll(async () => depth(window)).toBe(0);
  await gw.descendCell(cx, cy);
  await expect.poll(async () => depth(window)).toBe(1);
  await expect.poll(async () => (await gw.panes()).length).toBe(2);
});
