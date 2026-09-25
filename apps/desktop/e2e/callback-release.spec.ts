import { test, expect } from './fixtures';

// Every host callback the shim arms once releases its js.Func when it fires.
// syscall/js holds an unreleased one in its funcs map for the life of the
// page, and motion arms one per animation frame, so the leak grows with the
// gesture. Neither side of the boundary shows it, which is why the count is
// the assertion.

test('a gesture and its animation leave no armed callback behind', async ({ gw }) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);
  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  await gw.waitIdle();

  const before = await gw.oneShots();
  await gw.dragTileCell(cx, cy, cx + 2, cy + 1);
  await gw.descendCell(cx + 2, cy + 1);
  await gw.ascendViaCrumb();
  const after = await gw.oneShots();

  // The frames really go through the self-releasing form: every frame the
  // gesture drew armed one. How many frames that is belongs to the host (a
  // CI display draws a handful where a desktop draws sixty), so the count is
  // compared to the frames, not to a number.
  const frames = after.frames - before.frames;
  expect(frames, 'the gesture drew frames').toBeGreaterThan(0);
  expect(after.armed - before.armed, 'frames armed one-shot callbacks').toBeGreaterThanOrEqual(frames);
  // Settle timers armed by the gesture are one-shots too, so the count falls
  // to zero only once they have fired.
  await expect.poll(async () => (await gw.oneShots()).live, { timeout: 15_000 }).toBe(0);
});

// The persisters are armed once per frame, and a workspace holding a live
// shell draws them on the mirror's cadence with nobody touching it. Armed by
// the frame, their settle window could never close, and the pane tile's layout
// never reached the server at all. Armed by what they would write, an idle
// live tile holds no timer — and a pending settle timer is exactly what a
// non-zero live count means here.
test('a live tile repainting leaves no settle timer armed', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);
  await gw.openPalette();
  await gw.dragCreate('pane', cx, cy);
  await gw.descendCell(cx, cy);
  await gw.clickPaletteSwatch('shell');
  await expect
    .poll(() => window.evaluate(() => (window as any).__gridwellTest.shellRenderer()), {
      timeout: 15_000,
    })
    .toBe('webgl');
  await gw.waitIdle();

  // The premise: the mirror is still passing over an attached live surface,
  // which is what repaints the canvas with nobody touching it. The repaint is
  // a direct draw and not a scheduled frame, so the pass is what can be seen
  // from here; without it the zero below would mean nothing.
  const passes = await gw.shellMirrors();
  await expect
    .poll(() => gw.shellMirrors(), {
      message: 'the mirror stopped passing, so this proves nothing',
      timeout: 15_000,
    })
    .toBeGreaterThan(passes + 4);

  await expect
    .poll(async () => (await gw.oneShots()).live, {
      message: 'a settle timer is armed while nothing changes',
      timeout: 15_000,
    })
    .toBe(0);
});
