import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// A gesture's release can go missing: the button comes up outside the window,
// or over a surface that swallows the event. onMouseMove recovers from that —
// a move reporting the gesture's own button already up IS the release, late —
// but it only did so for the two gestures whose recovery was hand-written at
// their own branch: the left divider resize and the right-button drag. The left
// tile drag, which is the same class of state, was simply forgotten. Its lost
// release left a.dragging armed forever: an immortal ghost, the source tile
// hidden underneath it (ghost.hiddenTileID), waitIdle never returning, and
// every live view parked, since liveOverlaysHidden reads the same field.
//
// recoverLostRelease is now the one owner for all three, and each state
// finishes through its own commit path rather than being cleared — so this spec
// asserts the drop actually landed on the server, not merely that the state
// went quiet.
test('a left drag whose release is never seen still commits and unhides its tile', async ({
  window,
  gw,
}) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);

  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);

  const tile = tileAt(await gw.getGrid(home.gridID), 'text', cx, cy);
  expect(tile, 'the tile landed on the cell we dragged it to').toBeTruthy();
  const tileID = tile!.id;

  const from = await gw.cellCenter(home.id, cx, cy);
  const to = await gw.cellCenter(home.id, cx + 1, cy);

  // Press and drag past the threshold: the ghost materializes and hides the
  // source tile it stands in for.
  await window.mouse.move(from.x, from.y);
  await window.mouse.down();
  await window.mouse.move(from.x + 8, from.y + 8);
  await expect
    .poll(() => window.evaluate(() => (window as any).__gridwellTest.ghost().hiddenTileID), {
      message: 'the armed drag hides the source tile',
      timeout: 5_000,
    })
    .toBe(tileID);

  // The release we never see: a move over the target that reports no button
  // held, and no mouseup at all. This is what the window hears when the button
  // came up somewhere else.
  await window.evaluate(
    ([tx, ty]: number[]) => {
      document.querySelector('canvas')!.dispatchEvent(
        new MouseEvent('mousemove', { clientX: tx, clientY: ty, buttons: 0, bubbles: true }),
      );
    },
    [to.x, to.y],
  );

  // Nothing stays armed …
  await expect
    .poll(() => window.evaluate(() => (window as any).__gridwellTest.idleDetail().dragging), {
      message: 'the inferred release resolves the drag',
      timeout: 5_000,
    })
    .toBe(false);

  // … the source tile draws again (the ghost dies with the drop animation) …
  await expect
    .poll(() => window.evaluate(() => (window as any).__gridwellTest.ghost().hiddenTileID), {
      message: 'the source tile is no longer hidden under a ghost',
      timeout: 5_000,
    })
    .toBe('');

  // … and it resolved through the real commit path: the drop landed.
  await expect
    .poll(
      async () => tileAt(await gw.getGrid(home.gridID), 'text', cx + 1, cy)?.id ?? 'not there',
      { message: 'the recovered release committed the move', timeout: 10_000 },
    )
    .toBe(tileID);

  // Let the physical button go; by now there is nothing left for it to end.
  await window.mouse.up();
  await gw.waitIdle();
});
