import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Issue #242 (owner reversal of the 2026-07-10 organize-this default): the
// FIRST descent into a never-arranged pane tile CAPTURES the current
// window layout — you keep looking at exactly what you had, now inside the
// workspace — and the capture persists as the tile's arrangement with no
// save gesture. A single-pane window degenerates to the old default, which
// is what the roundtrip/bar specs continue to pin.

async function depth(window: any): Promise<number> {
  return window.evaluate(() => (window as any).__gridwellTest.workspace().depth);
}

test('first descent captures the current split; later descents restore it', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('local');
  await gw.splitFocusedPaneVertical();
  const panes0 = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  expect(panes0).toHaveLength(2);
  const rootGrid = panes0[0].gridID;

  // Drop the pane tile in the LEFT pane and descend into it.
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

  // The capture: BOTH panes survive into the workspace, framing the same
  // grid — not the old one-pane organize-this default.
  const inner = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  expect(inner, 'the split was captured').toHaveLength(2);
  for (const p of inner) {
    expect(p.gridID, 'captured panes keep their grids').toBe(rootGrid);
  }

  // The capture persists with no gesture (the layout persister's first
  // tick writes it — baseline nil for a never-arranged tile).
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

  // Leave (waitIdle: the return animation swallows clicks) and re-enter:
  // an ordinary blob descent restores the captured split.
  await gw.leaveWorkspace();
  await expect.poll(async () => depth(window)).toBe(0);
  await gw.descendCell(cx, cy);
  await expect.poll(async () => depth(window)).toBe(1);
  await expect.poll(async () => (await gw.panes()).length).toBe(2);
});
