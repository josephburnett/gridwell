import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// I11, the injection half (issue #5): an SSE event landing MID-TRANSITION
// must update tile DATA and never move the FRAMING the animation owns —
// "events own data, the animation owns framing; the two never cross."
// The separation was verified by inspection only; this drives it through the
// real stack: the transition clock is stretched via the e2e-only
// setTransitionMs hook, a tile is created through the server's front door
// while the descent animation is provably in flight (transitioning()), and
// the landing framing must be identical to an uninjected control descent —
// while the injected tile still shows up (the data DID fan out).

async function hook<T>(window: any, expr: string): Promise<T> {
  return window.evaluate(`(window).__gridwellTest.${expr}`);
}

test('an SSE event mid-descent updates data without deflecting the landing framing', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const a = await gw.focused();
  const cx = Math.round(a.cx);
  const cy = Math.round(a.cy) - 1;
  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  const well = tileAt(await gw.getGrid(a.gridID), 'well', cx, cy)!;
  const child = well.childGridId as string;
  const origin = await hook<string>(window, 'origin()');

  // CONTROL: a clean descent (no injection) fixes the expected landing.
  await gw.descendCell(cx, cy);
  const control = await gw.focused();
  {
    const f = await gw.focused();
    await gw.middleClickCell(Math.round(f.cx), Math.round(f.cy) + 1);
  }
  await gw.waitIdle();

  // Stretch the transition so the injection window is wide and deterministic.
  await hook(window, 'setTransitionMs(2000)');

  // Start the descent WITHOUT waiting for idle, then create a tile in the
  // child grid through the front door while the animation runs.
  const c = await gw.cellCenter(a.id, cx, cy);
  await window.mouse.click(c.x, c.y);
  const resp = await window.evaluate(
    async ([org, gridId, wellId]: string[]) => {
      const r = await fetch(`${org}/gridwell.v1.Gridwell/CreateTile`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Connect-Protocol-Version': '1' },
        body: JSON.stringify({
          gridId,
          path: { wellIds: [wellId] },
          tile: { kind: 'text', x: 0, y: 0, w: 1, h: 1 },
          data: btoa('# injected mid-flight'),
        }),
      });
      return { ok: r.ok, transitioning: (window as any).__gridwellTest.transitioning() };
    },
    [origin, child, well.id],
  );
  expect(resp.ok, 'injection CreateTile succeeded').toBe(true);
  expect(resp.transitioning, 'the event landed while the transition was in flight').toBe(true);

  await hook(window, 'setTransitionMs(350)');
  await gw.waitIdle();

  // Framing: the injected event must not have deflected the landing — the
  // pane sits exactly where the clean control descent landed.
  const landed = await gw.focused();
  expect(landed.gridID, 'descent completed into the child grid').toBe(child);
  expect(landed.cx, 'landing cx unchanged by the mid-flight event').toBeCloseTo(control.cx, 6);
  expect(landed.cy, 'landing cy unchanged by the mid-flight event').toBeCloseTo(control.cy, 6);
  expect(landed.zoom, 'landing zoom unchanged by the mid-flight event').toBeCloseTo(control.zoom, 6);

  // Data: the injected tile fanned out and is visible in the descended pane.
  expect(landed.tileIds, 'the injected tile reached the animating pane').toContainEqual(
    expect.stringMatching(/\/\d+$/),
  );
  const inChild = await gw.getGrid(child);
  expect((inChild.tiles ?? []).length, 'the injected tile exists in the child grid').toBe(1);
});
