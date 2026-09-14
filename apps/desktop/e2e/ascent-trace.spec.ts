import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// After an ascent a yellow outline marks the tile the pane just came out of,
// so the user can tell which shell or well they left. It arms when the ascent
// transition lands, fades over client/cadence's TraceFadeMs, and expires. It is
// view state and nothing is persisted.

async function traces(window: any) {
  return window.evaluate(() => (window as any).__gridwellTest.traces());
}

test('ascending arms a fading trace on the tile just left, then it expires', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);

  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  const well = tileAt(await gw.getGrid(home.gridID), 'well', cx, cy)!;

  await gw.descendCell(cx, cy);
  await expect.poll(async () => (await gw.focused()).gridID).toBe(well.childGridId);
  await gw.middleClickCell(cx, cy);
  await gw.waitIdle();

  const armed = await traces(window);
  expect(armed.length, 'one trace armed after the ascent').toBe(1);
  expect(armed[0].tileId, 'trace points at the well just left').toBe(well.id);
  expect(armed[0].paneId).toBe((await gw.focused()).id);
  expect(armed[0].alpha).toBeGreaterThan(0.3);

  // The fade is the client's own clock and nothing waits on it, so two samples
  // a known gap apart give the duration back: alpha is (1 - elapsed/dur)
  // squared, so the roots fall by gap/dur. Reading a length the client never
  // states is what makes a retuned fade fail here instead of passing quietly.
  const c = await gw.cadences();
  const sample = async () =>
    window.evaluate(() => ({
      at: Date.now(),
      alpha: (window as any).__gridwellTest.traces()[0]?.alpha ?? 0,
    }));
  const first = await sample();
  await window.waitForTimeout(c.traceFadeMs / 4);
  const second = await sample();
  expect(second.alpha, 'the trace is still fading a quarter of the way in').toBeGreaterThan(0);
  const measured = (second.at - first.at) / (Math.sqrt(first.alpha) - Math.sqrt(second.alpha));
  expect(measured, 'the fade runs for the declared duration').toBeGreaterThan(c.traceFadeMs * 0.75);
  expect(measured, 'the fade runs for the declared duration').toBeLessThan(c.traceFadeMs * 1.33);

  await expect
    .poll(async () => (await traces(window)).length, { timeout: c.traceFadeMs * 3 })
    .toBe(0);

  const after = tileAt(await gw.getGrid(home.gridID), 'well', cx, cy)!;
  expect(after.version, 'no version bump from the trace').toBe(well.version);
});
