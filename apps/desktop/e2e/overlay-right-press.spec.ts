import { test, expect } from './fixtures';

// A DOM overlay over a pane — the text editor's textarea, the shell's xterm
// host — hit-tests before the canvas, so a right press on one reaches Gridwell
// only because the overlay forwards it. Both forward through the same
// installer, so this spec drives the one gesture that proves the forward
// arrived: the right-drag that splits the pane. A split needs the press
// classified against the pane rect, so a forward that lost the coordinates or
// the button fails here too.

// A right-drag inward from the pane's left third: outside the swap third and
// outside the resize band, so it classifies as a split, and well inside the
// overlay, which stops short of the pane edge by a few pixels.
async function rightDragSplitFromOverlay(gw: any): Promise<number> {
  const p = await gw.focused();
  const y = p.y + p.h / 2;
  await gw.rightDragScreen(p.x + p.w * 0.15, y, p.x + p.w * 0.5, y);
  return (await gw.panes()).length;
}

test('a right-drag started on the text overlay splits the pane', async ({ gw }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  await gw.descendCell(cx, cy);
  await expect
    .poll(async () => (await gw.textareaInfo()) != null, { timeout: 10_000 })
    .toBe(true);

  expect(await rightDragSplitFromOverlay(gw), 'the press over the textarea armed the split').toBe(2);
});

test('a right-drag started on the shell overlay splits the pane', async ({ gw }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  await gw.openPalette();
  await gw.dragCreate('shell', cx, cy);
  // The drop lands bare; the descent opens the session and the xterm host.
  await gw.descendCell(cx, cy);
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');

  expect(await rightDragSplitFromOverlay(gw), 'the press over the xterm host armed the split').toBe(2);
});
