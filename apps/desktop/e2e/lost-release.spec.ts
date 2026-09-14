import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// A gesture's release can go missing: the button comes up outside the window,
// or over a surface that swallows the event. A move reporting the gesture's own
// button already up is that release, arriving late, and recoverLostRelease is
// the one owner of the inference for all three gestures that have it: the left
// tile drag, the left divider resize and the right-button drag. Each state
// finishes through its own commit path, so this spec asserts the drop landed on
// the server rather than that the state went quiet.
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

  // The create has settled — dragCreate ended on idle() — so the client holds
  // what the server holds. Asserted rather than assumed, because a press over
  // a tile the client's own cache is missing arms a pan, and from the ghost
  // alone that is indistinguishable from a press nothing took.
  const before = (await gw.panes()).find((p) => p.id === home.id)!;
  expect(
    before.tileIds,
    'the client received the tile the server has, before anything presses on it',
  ).toContain(tileID);

  // The whole gesture runs in one synchronous turn, and its mid-gesture state is
  // read inside that turn: a press, a move past the threshold so the ghost
  // materializes over the source tile, then a move over the target reporting no
  // button held and no mouseup, which is what the window hears when the button
  // came up somewhere else. It has to be one turn because Playwright's virtual
  // mouse leaves the real cursor parked with no button down, so a move Chromium
  // emits on its own between two evaluates carries buttons 0 and would end the
  // drag early. The cell centres are resolved in that same turn: a notice
  // raised between a separate read and the press moves every pane rect by half
  // the strip (errsurface.StripHeight), and the press would land on a layout
  // the read never saw.
  //
  // flake, 2026-09-04, closed: hiddenTileID stayed "" from the first poll in
  // three runs, in the window a peer session was rebuilding this press path in
  // the shared checkout every test launched from. A run now tests the artifacts
  // it copied at setup (e2e/runtree.ts); docs/flake-ledger.md carries the
  // evidence. armed returns the whole state so a repeat names what took the
  // press.
  const armed = await window.evaluate(
    ([paneID, ox, oy]: [string, number, number]) => {
      const t = (window as any).__gridwellTest;
      const canvas = document.querySelector('canvas')!;
      const from = t.cellCenter(paneID, ox, oy);
      const to = t.cellCenter(paneID, ox + 1, oy);
      const fire = (type: string, x: number, y: number, buttons: number) =>
        canvas.dispatchEvent(
          new MouseEvent(type, { clientX: x, clientY: y, buttons, button: 0, bubbles: true }),
        );
      fire('mousedown', from.x, from.y, 1);
      fire('mousemove', from.x + 8, from.y + 8, 1);
      const state = {
        ghost: t.ghost(),
        idle: t.idleDetail(),
        paletteOpen: t.palette().open,
        from,
        to,
        // The strip is layout: a notice up at the press moved every pane.
        stripH: t.errors().stripH,
        panes: t.panes().map((p: any) => ({
          id: p.id,
          gridID: p.gridID,
          x: p.x,
          y: p.y,
          w: p.w,
          h: p.h,
          // What tileAtCell reads. A press that found no tile at the cell and
          // a press another gesture took read identically from the ghost.
          tileIds: p.tileIds,
        })),
      };
      fire('mousemove', to.x, to.y, 0);
      return state;
    },
    [home.id, cx, cy] as [string, number, number],
  );
  expect(
    armed.ghost.hiddenTileID,
    `the armed drag hides the source tile — armed state: ${JSON.stringify(armed)}`,
  ).toBe(tileID);

  await expect
    .poll(() => window.evaluate(() => (window as any).__gridwellTest.idleDetail().dragging), {
      message: 'the inferred release resolves the drag',
      timeout: 5_000,
    })
    .toBe(false);

  await expect
    .poll(() => window.evaluate(() => (window as any).__gridwellTest.ghost().hiddenTileID), {
      message: 'the source tile is no longer hidden under a ghost',
      timeout: 5_000,
    })
    .toBe('');

  await expect
    .poll(
      async () => tileAt(await gw.getGrid(home.gridID), 'text', cx + 1, cy)?.id ?? 'not there',
      { message: 'the recovered release committed the move', timeout: 10_000 },
    )
    .toBe(tileID);

  await gw.waitIdle();
});
