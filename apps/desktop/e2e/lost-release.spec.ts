import { test, expect } from './fixtures';
import { tileAt, type Tile } from './oracle';

// A gesture's release can go missing: the button comes up outside the window,
// or over a surface that swallows the event. A move reporting the gesture's own
// button already up is that release, arriving late: gesture.RecoverRelease
// infers it for every gesture, and App.finishRelease finishes it as it does a
// mouseup. Each state finishes through its own commit path, so these specs
// assert what landed on the server rather than that the state went quiet.
test('a left drag whose release is never seen still commits and unhides its tile', async ({
  window,
  gw,
}) => {
  await gw.enterSource('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);

  await gw.openMenu();
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
  // the notice row (errsurface.NoticesHeight), and the press would land on a layout
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
        menuOpen: t.menu().open,
        from,
        to,
        // The notice row is layout: a notice up at the press moved every pane.
        noticesH: t.errors().noticesH,
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

// Every gesture lands where its release is learned, the move that reports the
// button up included, a resize as a drag does (App.finishRelease). Each runs
// in one turn, as above, and the late release comes well past the last move
// that held the button.
test('a divider resize whose release is never seen lands where the release is learned', async ({ window, gw }) => {
  await gw.enterSource('home');
  await gw.splitFocusedPaneVertical();
  const [left] = (await gw.panes()).slice().sort((a, b) => a.x - b.x);
  const x = left.x + left.w;
  const y = left.y + left.h / 2;
  const armed = await window.evaluate(
    ([x, y]: [number, number]) => {
      const t = (window as any).__gridwellTest;
      const canvas = document.querySelector('canvas')!;
      const fire = (type: string, cx: number, buttons: number) =>
        canvas.dispatchEvent(new MouseEvent(type, { clientX: cx, clientY: y, buttons, button: 0, bubbles: true }));
      fire('mousedown', x - 2, 1);
      fire('mousemove', x + 40, 1);
      const resizing = t.leftResizeArmed();
      fire('mousemove', x + 120, 0);
      return resizing;
    },
    [x, y] as [number, number],
  );
  expect(armed, 'the press grabbed the divider').toBe(true);
  await gw.waitIdle();
  const after = (await gw.panes()).find((p) => p.id === left.id)!;
  expect(Math.abs(after.w - (left.w + 122)), `the left pane is ${after.w}, from ${left.w}`).toBeLessThan(4);
});

test('a tile resize whose release is never seen lands where the release is learned', async ({ window, gw }) => {
  await gw.enterSource('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  await gw.openMenu();
  await gw.dragCreate('markdown', cx, cy);
  const before = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;
  expect(before, 'the tile landed').toBeTruthy();
  await gw.waitClientTileAt(f.id, cx, cy, before.id);

  const armed = await window.evaluate(
    ([paneID, x, y]: [string, number, number]) => {
      const t = (window as any).__gridwellTest;
      const canvas = document.querySelector('canvas')!;
      // A quarter cell past a cell's center, so a point MouseEvent truncates
      // to whole pixels still rounds to the cell after it.
      const at = (dx: number) => {
        const c = t.cellCenter(paneID, x + dx, y + dx);
        const n = t.cellCenter(paneID, x + dx + 1, y + dx + 1);
        return { x: c.x + 0.35 * (n.x - c.x), y: c.y + 0.35 * (n.y - c.y) };
      };
      const fire = (type: string, p: { x: number; y: number }, buttons: number) =>
        canvas.dispatchEvent(new MouseEvent(type, { clientX: p.x, clientY: p.y, buttons, button: 2, bubbles: true }));
      // Outside the center third, so the right press resizes.
      fire('mousedown', at(0), 2);
      fire('mousemove', at(1), 2);
      const kind = t.rightDrag();
      fire('mousemove', at(3), 0);
      return kind;
    },
    // The tile's bottom-right cell, so the press grabs that corner.
    [f.id, Number(before.x ?? 0) + Number(before.w ?? 1) - 1, Number(before.y ?? 0) + Number(before.h ?? 1) - 1] as [
      string,
      number,
      number,
    ],
  );
  expect(armed, 'the right press armed a gesture').toBeTruthy();
  await gw.waitIdle();
  await expect
    .poll(async () => {
      const t = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy);
      const edge = (r: Tile) => [Number(r.x ?? 0) + Number(r.w ?? 0), Number(r.y ?? 0) + Number(r.h ?? 0)];
      return t ? [edge(t)[0] - edge(before)[0], edge(t)[1] - edge(before)[1]] : null;
    }, { message: 'the moving corner followed the late release three cells' })
    .toEqual([3, 3]);
});
