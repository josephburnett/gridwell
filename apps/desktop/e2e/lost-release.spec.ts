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

  // The whole gesture in ONE synchronous turn, and its mid-gesture state read
  // inside that turn rather than polled for after it:
  //
  //   press → past the threshold (the ghost materializes and hides the source
  //   tile it stands in for) → the release we never see, a move over the
  //   target reporting no button held and no mouseup at all, which is what
  //   the window hears when the button came up somewhere else.
  //
  // One turn because this press does not exist in Chromium's own input state:
  // Playwright drives a virtual mouse, so the real cursor stays parked at the
  // screen center with no button down, and any move Chromium emits on its own
  // — a layer change under that cursor — carries buttons 0, which IS a lost
  // release and would end this drag early. Between two evaluates there is a
  // window for one; inside a turn there is none, and a user's real press,
  // which the platform knows about, has no such ambiguity to begin with.
  //
  // (flake, 2026-09-04: three runs saw hiddenTileID stay "" from the first
  // poll onward — a ghost that never existed, not one that armed and died.
  // Unreproduced since on a freshly built tree; docs/flake-ledger.md carries
  // the evidence. Hence armed: the whole state comes back, so a repeat names
  // what took the press instead of reporting an empty string.)
  const armed = await window.evaluate(
    ([fx, fy, tx, ty]: number[]) => {
      const t = (window as any).__gridwellTest;
      const canvas = document.querySelector('canvas')!;
      const fire = (type: string, x: number, y: number, buttons: number) =>
        canvas.dispatchEvent(
          new MouseEvent(type, { clientX: x, clientY: y, buttons, button: 0, bubbles: true }),
        );
      fire('mousedown', fx, fy, 1);
      fire('mousemove', fx + 8, fy + 8, 1);
      const state = {
        ghost: t.ghost(),
        idle: t.idleDetail(),
        paletteOpen: t.palette().open,
        panes: t.panes().map((p: any) => ({ id: p.id, gridID: p.gridID, x: p.x, y: p.y, w: p.w, h: p.h })),
      };
      fire('mousemove', tx, ty, 0);
      return state;
    },
    [from.x, from.y, to.x, to.y],
  );
  expect(
    armed.ghost.hiddenTileID,
    `the armed drag hides the source tile — armed state: ${JSON.stringify(armed)}`,
  ).toBe(tileID);

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

  await gw.waitIdle();
});
