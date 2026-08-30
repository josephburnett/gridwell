import { test as base, expect, Page } from '@playwright/test';
import { ChildProcess } from 'node:child_process';
import * as fs from 'node:fs';
import { seedHome } from '../e2e/fixtures';
import { Served, spawnServe, freePort, authenticate } from './fixtures';
import { GridwellDriver } from '../e2e/driver';
import { tileAt } from '../e2e/oracle';

// The mid-session outage seam: the only gate that kills the server under a live
// client. Without it, a network blip during autosave can destroy the only copy
// of unsaved text and a reconnect can render stale state silently. This spec is
// the end-to-end oracle for that class: type into a doc, kill the server
// mid-session, keep the typing on screen, restart the server on the same port,
// and prove the retry kick lands the save on the reborn server with no user
// action.
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
