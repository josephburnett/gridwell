import { test as base, expect, Page } from '@playwright/test';
import { ChildProcess } from 'node:child_process';
import * as fs from 'node:fs';
import { seedHome } from '../e2e/fixtures';
import { Served, spawnServe, freePort, authenticate } from './fixtures';
import { GridwellDriver } from '../e2e/driver';
import { tileAt } from '../e2e/oracle';

// The mid-session outage seam, the only gate that takes the link away under a
// live client: a blip during autosave can destroy the only copy of unsaved
// text, and a request the network swallows can leave a pane loading forever.
// These specs kill the server mid-session and prove the retry kick lands the
// save on the reborn server with no user action, or swallow one request without
// killing anything and prove the client recovers on its own. The stock `serve`
// fixture cannot revive its process, so this file owns a restartable server.



class RestartableServe {
  child: ChildProcess | null = null;
  token = '';
  constructor(
    readonly home: string,
    readonly port: number,
  ) {}
  get origin(): string {
    return `http://127.0.0.1:${this.port}`;
  }
  get served(): Served {
    return { origin: this.origin, home: this.home, token: this.token, child: this.child! };
  }
  async start(): Promise<void> {
    const served = await spawnServe(this.home, this.port);
    this.child = served.child;
    this.token = served.token; // the same password gives the same token across restarts
  }
  // SIGKILL, so there is no goodbye on any stream: the shape of a dropped link.
  // It waits for the exit so the port is genuinely free for the restart.
  async kill(): Promise<void> {
    const child = this.child;
    this.child = null;
    if (!child) return;
    await new Promise<void>((resolve) => {
      child.once('exit', () => resolve());
      child.kill('SIGKILL');
    });
  }
}

type Fixtures = {
  outage: RestartableServe;
  window: Page;
  gw: GridwellDriver;
};

const test = base.extend<Fixtures>({
  outage: async ({}, use) => {
    const home = seedHome();
    const srv = new RestartableServe(home, await freePort());
    await srv.start();
    await use(srv);
    await srv.kill();
    fs.rmSync(home, { recursive: true, force: true });
  },
  window: async ({ outage, page }, use) => {
    await authenticate(page, outage.served);
    await page.goto(outage.origin + '/?e2e=1');
    await page.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
    await use(page);
  },
  gw: async ({ window, outage }, use) => {
    await use(new GridwellDriver(window, outage.origin));
  },
});

test('typing survives a server outage and saves itself after the restart', async ({
  gw,
  window,
  outage,
}) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const created = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;
  await gw.descendCell(cx, cy);

  // A saved baseline, so the outage save is a versioned edit, not a first write.
  await gw.typeText('saved before the outage. ');
  await expect
    .poll(async () => gw.getTileContent(created.id), { timeout: 10_000 })
    .toContain('saved before the outage.');

  // The debounced autosave fires into a dead socket.
  await outage.kill();
  await gw.typeText('typed while the server was dead.');

  // A failed save that drops the buffer drops the only copy, and the next
  // render repaints the textarea from stale bytes.
  await expect
    .poll(
      async () =>
        window.evaluate(() => {
          const ta = document.querySelector('textarea');
          return ta ? (ta as HTMLTextAreaElement).value : '';
        }),
      { timeout: 15_000 },
    )
    .toContain('typed while the server was dead.');

  // Some notice is on the strip; which one is not pinned here.
  await expect
    .poll(async () => {
      const errs = await window.evaluate(() => (window as any).__gridwellTest.errors());
      return errs.notices.length;
    })
    .toBeGreaterThan(0);

  // Same port, same home. No user action from here on: the reconnect kick and
  // the flush sweep must land the dirty buffer by themselves.
  await outage.start();
  await expect
    .poll(async () => gw.getTileContent(created.id), { timeout: 30_000 })
    .toContain('typed while the server was dead.');

  // No revert and no double-apply.
  const value = await window.evaluate(() => {
    const ta = document.querySelector('textarea');
    return ta ? (ta as HTMLTextAreaElement).value : '';
  });
  expect(value).toContain('saved before the outage.');
  expect(value).toContain('typed while the server was dead.');
});

test('a grid fetch the network swallows does not latch "loading" forever', async ({
  gw,
  window,
  outage,
}) => {
  // The client dedupes GetGrid per grid id and releases the claim when the
  // request returns. A killed server answers with a reset, so the request does
  // return; the shape that hurts is the black hole, where it neither answers
  // nor fails, and a claim never released means no refetch, no error, and
  // "loading …" for the life of the page. Nothing here restarts the server: a
  // fetch is bounded, so the pane comes back off its own clock.
  test.setTimeout(150_000);
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  const well = tileAt(await gw.getGrid(f.gridID), 'well', cx, cy)!;
  const child = well.childGridId as string;

  // An empty child grid signs the same as an uncached one, so without a tile
  // inside "the grid came back" is unobservable.
  await gw.descendCell(cx, cy);
  const inner = await gw.focused();
  const icx = Math.round(inner.cx);
  const icy = Math.round(inner.cy);
  await gw.openPalette();
  await gw.dragCreate('markdown', icx, icy);
  const note = tileAt(await gw.getGrid(child), 'text', icx, icy)!;
  expect(note, 'the well holds a tile to come back to').toBeTruthy();

  // The next GetGrid for this one grid is never fulfilled and never aborted.
  // Everything else keeps working, so the only thing between the pane and its
  // grid is the client's own claim on that id.
  let blackhole = true;
  await window.route('**/gridwell.v1.Gridwell/GetGrid', async (route) => {
    if (blackhole && (route.request().postData() ?? '').includes(child)) {
      blackhole = false; // one request into the hole; the retry gets a live link
      return;
    }
    await route.continue();
  });

  // A fresh boot, so the child grid is out of the cache and the well's preview
  // asks again, into the hole.
  await window.goto(outage.origin + '/?e2e=1');
  await window.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
  const home = await gw.focused();
  const c = await gw.cellCenter(home.id, cx, cy);
  await window.mouse.click(c.x, c.y); // descend; no waitIdle, the fetch is hung by design

  await expect
    .poll(async () => (await gw.focused()).gridID, { timeout: 15_000 })
    .toBe(child);
  await expect
    .poll(
      async () =>
        (await window.evaluate(() => (window as any).__gridwellTest.idleDetail())).gridInflight,
      { message: 'the grid fetch is in flight and stuck there', timeout: 15_000 },
    )
    .toContain(child);
  const stuck = await window.evaluate(
    (gid: string) => Object.keys((window as any).__gridwellTest.gridSigs(gid)).length,
    child,
  );
  expect(stuck, 'the pane is showing a grid it does not have').toBe(0);

  // No user action, no restart, no reconnect: only the bounded fetch giving up.
  await expect
    .poll(
      async () =>
        Object.keys(
          await window.evaluate(
            (gid: string) => (window as any).__gridwellTest.gridSigs(gid),
            child,
          ),
        ),
      { message: 'the grid the pane is showing loads itself again', timeout: 45_000 },
    )
    .toContain(note.id);
  expect(
    await window.evaluate(() => (window as any).__gridwellTest.idleDetail()),
    'the claim the zombie held is gone with it',
  ).toMatchObject({ gridInflight: [] });
});

test('framing settled during an outage lands after the restart', async ({ gw, outage }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  // A fresh well's zoom stays 0 until the first framing write, the oracle
  // framing-roundtrip.spec.ts settles with.
  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  const well = tileAt(await gw.getGrid(f.gridID), 'well', cx, cy)!;
  await gw.descendCell(cx, cy);
  const inside = await gw.focused();

  await outage.kill();

  // The settle persister fires SetFraming into the dead socket, and the outbox
  // must park it, or the optimistic patch survives on screen unposted.
  await gw.panFocusedGrid(
    Math.round(inside.cx) + 1,
    Math.round(inside.cy),
    Math.round(inside.cx) - 1,
    Math.round(inside.cy),
  );

  await outage.start();

  // The parked write lands with no user action, so the zoom leaves its 0.
  await expect
    .poll(
      async () => {
        const g = await gw.getGrid(f.gridID);
        const t = (g.tiles ?? []).find((t) => t.id === well.id);
        return Number((t as { viewZoom?: number | string } | undefined)?.viewZoom ?? 0);
      },
      { timeout: 30_000 },
    )
    .toBeGreaterThan(0);
});

// The ephemeral cleanup is the one delete with nothing on screen to reconcile:
// the row is off-grid and a shell's tmux session lives behind it. Ascending
// deletes it, so the delete has to park like every other unacknowledged write.
// One sent outside the dispatcher would be lost to an outage at that moment.
test('an ephemeral visit left during an outage parks its delete and drains on reconnect', async ({
  gw,
  window,
  outage,
}) => {
  const scratchGridID = (await gw.plugins()).find((l) => l.kind === 'home')!.scratchGridID;
  expect(scratchGridID, 'the home plugin advertises a scratch grid').toBeTruthy();

  await gw.enterPlugin('home');

  // Shells ride the web door, so this is the browser's own visit.
  await gw.clickPaletteSwatch('shell');
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  const ephemeralID = (await gw.focused()).textFocus;
  expect(
    ((await gw.getGrid(scratchGridID)).tiles ?? []).length,
    'the scratch row is there to be cleaned up',
  ).toBe(1);

  // The ascent's delete fires into a dead socket.
  await outage.kill();
  await gw.ascendViaCrumb();

  // It parked instead of evaporating.
  await expect
    .poll(() => window.evaluate(() => (window as any).__gridwellTest.outbox()), { timeout: 15_000 })
    .toContain('DeleteTile:' + ephemeralID);

  // The reconnect drains it with no user action, and the tmux session goes too.
  await outage.start();
  await expect
    .poll(async () => ((await gw.getGrid(scratchGridID)).tiles ?? []).length, { timeout: 30_000 })
    .toBe(0);
});

// The swallowed write, the mirror of the swallowed read above. Killing a server
// resets its sockets, so every write returns; the shape that hurts is the black
// hole, where an outbox that recorded a write on its RETURN would have no entry
// and no drain for the one write it exists for. Nothing is killed here: the
// write parks the moment it is sent and the retry drains it.
test('a write the network swallows parks in the outbox and drains itself', async ({
  gw,
  window,
}) => {
  test.setTimeout(150_000);
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  // A fresh well's zoom stays 0 until the first framing write.
  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  const well = tileAt(await gw.getGrid(f.gridID), 'well', cx, cy)!;
  await gw.descendCell(cx, cy);
  const inside = await gw.focused();

  // Every SetFraming for this well hangs until the test opens the route, so the
  // only thing between the viewport and the server is the client's bookkeeping.
  let blackhole = true;
  await window.route('**/gridwell.v1.Gridwell/SetFraming', async (route) => {
    if (blackhole && (route.request().postData() ?? '').includes(well.id)) return;
    await route.continue();
  });

  // The settle persister fires SetFraming into the hole.
  await gw.panFocusedGrid(
    Math.round(inside.cx) + 1,
    Math.round(inside.cy),
    Math.round(inside.cx) - 1,
    Math.round(inside.cy),
  );

  // While it hangs the write is owed a verdict and the outbox says so; one
  // recorded on its return would be invisible here.
  await expect
    .poll(() => window.evaluate(() => (window as any).__gridwellTest.outbox()), {
      message: 'the swallowed write is parked while it waits',
      timeout: 20_000,
    })
    .toContain('SetFraming:' + well.id);

  // With no restart and no reconnect, the drain is the only thing left.
  blackhole = false;

  await expect
    .poll(
      async () => {
        const g = await gw.getGrid(f.gridID);
        const t = (g.tiles ?? []).find((t) => t.id === well.id);
        return Number((t as { viewZoom?: number | string } | undefined)?.viewZoom ?? 0);
      },
      { message: 'the parked write reaches the server on its own', timeout: 90_000 },
    )
    .toBeGreaterThan(0);
});

// The backstop's own clock. The test above proves a parked write drains
// itself; nothing in it would notice if the interval were a minute or a
// millisecond, and docs/freshness.md quotes the cadence as a guarantee. This
// one retunes `retry.Backstop` through the e2e hook and bounds the re-post
// from both sides. The ephemeral delete is the write, because it is sent once
// on the ascent and nothing but the backstop sends it again: a framing write
// settles more than once after a pan, so a landed row would not say who landed
// it. The route aborts rather than hanging — the same dropped link a killed
// server gives, with no request left in flight to hang the idle wait.
test('the backstop re-posts a parked write on its interval, not before', async ({
  gw,
  window,
}) => {
  const scratchGridID = (await gw.plugins()).find((l) => l.kind === 'home')!.scratchGridID;
  await gw.enterPlugin('home');

  await gw.clickPaletteSwatch('shell');
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  const ephemeralID = (await gw.focused()).textFocus;

  const rows = async () => ((await gw.getGrid(scratchGridID)).tiles ?? []).length;
  const parked = () => window.evaluate(() => (window as any).__gridwellTest.outbox());
  expect(await rows(), 'the scratch row is there to be cleaned up').toBe(1);

  let dropped = true;
  await window.route('**/gridwell.v1.Gridwell/DeleteTile', async (route) => {
    if (dropped && (route.request().postData() ?? '').includes(ephemeralID)) return route.abort();
    await route.continue();
  });

  // The ascent's delete fires into the dropped link and parks.
  await gw.ascendViaCrumb();
  await expect
    .poll(parked, { message: 'the dropped delete is parked', timeout: 20_000 })
    .toContain('DeleteTile:' + ephemeralID);

  // Retuning restarts the wait in flight, so the next re-post is this far from
  // here and no earlier landing can be a tick that was already due.
  expect(
    await window.evaluate((ms: number) => (window as any).__gridwellTest.setBackstopMs(ms), 20_000),
    'the cadence the client is running on',
  ).toBe(20_000);

  // The link is open from here on, so the only thing between the parked delete
  // and the server is the backstop.
  dropped = false;

  await window.waitForTimeout(6_000);
  expect(await rows(), 'nothing re-posts inside the interval').toBe(1);
  expect(await parked(), 'the delete is still owed a verdict').toContain(
    'DeleteTile:' + ephemeralID,
  );

  // And within: one tick of the lowered interval lands it, with no user action.
  await window.evaluate(() => (window as any).__gridwellTest.setBackstopMs(1_000));
  await expect
    .poll(rows, {
      // Tight, so a client still running the unlowered cadence cannot pass by
      // having a tick of its own fall inside the window.
      message: 'the parked delete reaches the server on the next backstop tick',
      timeout: 8_000,
    })
    .toBe(0);
  await expect.poll(parked, { timeout: 10_000 }).not.toContain('DeleteTile:' + ephemeralID);
});
