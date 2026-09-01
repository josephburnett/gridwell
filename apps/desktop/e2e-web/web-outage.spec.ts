import { test as base, expect, Page } from '@playwright/test';
import { ChildProcess } from 'node:child_process';
import * as fs from 'node:fs';
import { seedHome } from '../e2e/fixtures';
import { Served, spawnServe, freePort, authenticate } from './fixtures';
import { GridwellDriver } from '../e2e/driver';
import { tileAt } from '../e2e/oracle';

// The mid-session outage seam: the only gate that takes the link away under a
// live client. Without it, a network blip during autosave can destroy the only
// copy of unsaved text, a reconnect can render stale state silently, and a
// request the network swallows can leave a pane loading forever. These specs
// are the end-to-end oracle for that class: type into a doc, kill the server
// mid-session, keep the typing on screen, restart the server on the same port,
// and prove the retry kick lands the save on the reborn server with no user
// action — and, without killing anything, swallow one grid read and prove the
// pane comes back on its own.
//
// The stock `serve` fixture cannot revive its process, so this spec owns a
// restartable server: the same seedHome, binary, and flags, plus kill() and
// start() the test drives.



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
  // kill is the outage: SIGKILL, so there is no graceful shutdown and no goodbye
  // on any stream, the shape of a dropped link or a crashed machine. It waits
  // for the exit so the port is genuinely free for the restart.
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

  // Seed a saved baseline so the outage save is a real versioned edit rather than
  // a first write.
  await gw.typeText('saved before the outage. ');
  await expect
    .poll(async () => gw.getTileContent(created.id), { timeout: 10_000 })
    .toContain('saved before the outage.');

  // The outage: kill the server, then type. The debounced autosave fires into a
  // dead socket.
  await outage.kill();
  await gw.typeText('typed while the server was dead.');

  // The typing must stay on screen and stay dirty. A failed save that drops the
  // buffer drops the only copy, and the next render repaints the textarea from
  // stale bytes.
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

  // And the failure surfaces rather than staying silent: some notice is on the
  // strip, a save retry or a disconnect. Which one is not pinned here.
  await expect
    .poll(async () => {
      const errs = await window.evaluate(() => (window as any).__gridwellTest.errors());
      return errs.notices.length;
    })
    .toBeGreaterThan(0);

  // The recovery: same port, same home. No user action from here on; the
  // reconnect kick, which re-opens the event stream, plus the flush sweep must
  // land the dirty buffer on the reborn server by themselves.
  await outage.start();
  await expect
    .poll(async () => gw.getTileContent(created.id), { timeout: 30_000 })
    .toContain('typed while the server was dead.');

  // The screen still shows the full document: no revert, no double-apply.
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
  // The zombie fetch, and the pane that waits on it forever. The client dedupes
  // GetGrid per grid id, so the per-frame draw cannot dogpile the server, and
  // the claim is released when the request returns. A killed server answers
  // with a reset and the request does return; the shape that hurts is the
  // black hole — a laptop asleep, a route that went away — where the request
  // neither answers nor fails. The claim was then never released: no refetch
  // ever fired, no error was ever reported, and the pane said "loading …" for
  // the life of the page. That is what the gitlab well did on 2026-08-31,
  // while the plugin was answering fine.
  //
  // Nothing here restarts the server, because nothing needs to: the fix is that
  // a fetch is bounded, so the pane comes back on its own, off its own clock,
  // with the link in exactly the state that broke it. (The event stream's
  // reconnect resync also cancels in-flight fetches, which is faster when the
  // stream does come back — client/inflight's unit tests own that half.)
  test.setTimeout(150_000);
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  const well = tileAt(await gw.getGrid(f.gridID), 'well', cx, cy)!;
  const child = well.childGridId as string;

  // A tile inside the well: an empty child grid signs the same as an uncached
  // one, so without it "the grid came back" is unobservable.
  await gw.descendCell(cx, cy);
  const inner = await gw.focused();
  const icx = Math.round(inner.cx);
  const icy = Math.round(inner.cy);
  await gw.openPalette();
  await gw.dragCreate('markdown', icx, icy);
  const note = tileAt(await gw.getGrid(child), 'text', icx, icy)!;
  expect(note, 'the well holds a tile to come back to').toBeTruthy();

  // The black hole: the next GetGrid for this one grid is swallowed — never
  // fulfilled, never aborted. Every other request keeps working, the way a dead
  // socket leaves a fresh one fine, and so does the retry, so the only thing
  // between the pane and its grid is the client's own claim on that id.
  let blackhole = true;
  await window.route('**/gridwell.v1.Gridwell/GetGrid', async (route) => {
    if (blackhole && (route.request().postData() ?? '').includes(child)) {
      blackhole = false; // one request into the hole; the retry gets a live link
      return;
    }
    await route.continue();
  });

  // Back to a fresh boot at home, so the child grid is out of the cache and the
  // well's preview asks for it again — into the hole.
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

  // No user action, no restart, no reconnect: only time. The bounded fetch
  // gives up, says so, and the next draw asks again over a link that works.
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

  // A fresh well: its zoom stays 0 until the first framing write, the same oracle
  // framing-roundtrip.spec.ts settles with.
  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  const well = tileAt(await gw.getGrid(f.gridID), 'well', cx, cy)!;
  await gw.descendCell(cx, cy);
  const inside = await gw.focused();

  await outage.kill();

  // Pan inside the well: the settle persister fires SetFraming into the dead
  // socket, and the outbox must park it. Otherwise the optimistic patch survives
  // on screen and nothing ever re-posts it.
  await gw.panFocusedGrid(
    Math.round(inside.cx) + 1,
    Math.round(inside.cy),
    Math.round(inside.cx) - 1,
    Math.round(inside.cy),
  );

  await outage.start();

  // The parked framing write lands on the reborn server with no user action: the
  // well row's zoom leaves its never-written 0.
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
