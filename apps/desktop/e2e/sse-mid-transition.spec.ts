import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// An event landing mid-transition must update tile data and never move the
// framing the animation owns. Events own data and the animation owns framing.
// The transition clock is stretched through the e2e-only setTransitionMs hook
// and a tile is created through the server's front door while transitioning()
// says the descent animation is in flight, so the landing framing must match an
// uninjected control descent while the injected tile still shows up.

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

  // A clean descent with no injection fixes the expected landing.
  await gw.descendCell(cx, cy);
  const control = await gw.focused();
  {
    const f = await gw.focused();
    await gw.middleClickCell(Math.round(f.cx), Math.round(f.cy) + 1);
  }
  await gw.waitIdle();

  // Stretch the transition so the injection window is wide and deterministic.
  await hook(window, 'setTransitionMs(2000)');

  // The descent starts without waiting for idle, so the CreateTile below lands
  // while the animation is still running.
  const c = await gw.cellCenter(a.id, cx, cy);
  await window.mouse.click(c.x, c.y);
  const resp = await window.evaluate(
    async ([org, gridId, wellId]: string[]) => {
      const r = await fetch(`${org}/gridwell.v1.Gridwell/CreateTile`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Connect-Protocol-Version': '1',
          // The door refuses a page's call that names no build (Server.staleBuild).
          'Gridwell-Build': (window as any).__gridwellTest.build(),
        },
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

  const landed = await gw.focused();
  expect(landed.gridID, 'descent completed into the child grid').toBe(child);
  expect(landed.cx, 'landing cx unchanged by the mid-flight event').toBeCloseTo(control.cx, 6);
  expect(landed.cy, 'landing cy unchanged by the mid-flight event').toBeCloseTo(control.cy, 6);
  expect(landed.zoom, 'landing zoom unchanged by the mid-flight event').toBeCloseTo(control.zoom, 6);

  // The tile arrives by its event, which waitIdle cannot wait for: landing
  // reads nothing the descent already read.
  await expect
    .poll(async () => (await gw.focused()).tileIds, {
      message: 'the injected tile reached the animating pane',
      timeout: 10_000,
    })
    .toContainEqual(expect.stringMatching(/\/(\d+|~[A-Za-z0-9_-]+)$/));
  const inChild = await gw.getGrid(child);
  expect((inChild.tiles ?? []).length, 'the injected tile exists in the child grid').toBe(1);
});

// The read of a grid and an event about it can cross on the wire: the answer
// is taken before a tile is created, the event arrives first, and the answer
// lands after, over the fold. So a tile event owes the grid's read in flight
// a re-ask (events.Owe); without that the stale answer stands and the tile
// never shows. The hold makes the crossing deterministic.
test('a tile event that crosses its grid read on the wire still reaches the pane', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const a = await gw.focused();
  const cx = Math.round(a.cx);
  const cy = Math.round(a.cy) - 1;
  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  const well = tileAt(await gw.getGrid(a.gridID), 'well', cx, cy)!;
  const child = well.childGridId as string;
  const origin = await hook<string>(window, 'origin()');

  // The descent's read of the child grid is answered now and held; the
  // answer predates the injection below. The well's preview already read the
  // grid, so the event folds into the cache and the held answer would land
  // over the fold.
  let release: (() => Promise<void>) | null = null;
  let childReads = 0;
  await window.route('**/gridwell.v1.Gridwell/GetGrid', async (r: any) => {
    if (!(r.request().postData() ?? '').includes(child)) {
      await r.continue();
      return;
    }
    childReads++;
    if (!release) {
      const resp = await r.fetch();
      const body = await resp.body();
      release = () => r.fulfill({ response: resp, body });
      return;
    }
    await r.continue();
  });
  try {
    const c = await gw.cellCenter(a.id, cx, cy);
    await window.mouse.click(c.x, c.y);
    await expect
      .poll(() => release !== null, { message: 'the child grid read is held on the wire', timeout: 10_000 })
      .toBe(true);
    const ok = await window.evaluate(
      async ([org, gridId, wellId]: string[]) => {
        const r = await fetch(`${org}/gridwell.v1.Gridwell/CreateTile`, {
          method: 'POST',
          headers: {
            'Content-Type': 'application/json',
            'Connect-Protocol-Version': '1',
            // The door refuses a page's call that names no build (Server.staleBuild).
            'Gridwell-Build': (window as any).__gridwellTest.build(),
          },
          body: JSON.stringify({
            gridId,
            path: { wellIds: [wellId] },
            tile: { kind: 'text', x: 0, y: 0, w: 1, h: 1 },
            data: btoa('# crossed the read'),
          }),
        });
        return r.ok;
      },
      [origin, child, well.id],
    );
    expect(ok, 'injection CreateTile succeeded').toBe(true);
    // The node publishes the tile's event before it answers the create; the
    // oracle round trip below is the event's time to reach the client.
    expect((await gw.getGrid(child)).tiles?.length, 'the node holds the injected tile').toBe(1);
    await window.waitForTimeout(300);
    await (release as unknown as () => Promise<void>)();
    // The owed re-ask is the fix under test; the route stays up until it has
    // been let through, or tearing the route down fails that read.
    await expect
      .poll(() => childReads, { message: 'the stale answer owed the grid a re-ask', timeout: 10_000 })
      .toBeGreaterThanOrEqual(2);
  } finally {
    await window.unroute('**/gridwell.v1.Gridwell/GetGrid');
  }

  await expect
    .poll(async () => (await gw.focused()).tileIds, {
      message: 'the injected tile reached the pane after the stale answer landed',
      timeout: 10_000,
    })
    .toContainEqual(expect.stringMatching(/\/(\d+|~[A-Za-z0-9_-]+)$/));
});
