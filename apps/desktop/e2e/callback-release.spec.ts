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

  // The frames really go through the self-releasing form. A drag plus two
  // transitions arms about 65 of them; the settle timers alone arm 5, which is
  // what the count falls to if the frame loop stops using it.
  expect(after.armed - before.armed, 'frames armed one-shot callbacks').toBeGreaterThan(20);
  // Settle timers armed by the gesture are one-shots too, so the count falls
  // to zero only once they have fired.
  await expect.poll(async () => (await gw.oneShots()).live, { timeout: 15_000 }).toBe(0);
});
