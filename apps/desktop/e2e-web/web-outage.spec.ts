import { test as base, expect, Page } from '@playwright/test';
import { ChildProcess } from 'node:child_process';
import * as fs from 'node:fs';
import { seedHome } from '../e2e/fixtures';
import { Served, spawnServe, freePort, authenticate } from './fixtures';
import { GridwellDriver } from '../e2e/driver';
import { tileAt } from '../e2e/oracle';

// The mid-session OUTAGE seam (2026-08-14, the transport-loss class):
// nothing in any gate ever killed the server under a live client, which is
// exactly why a wifi blip during autosave could destroy the only copy of
// unsaved text (the archetype bug) and a reconnect silently rendered stale
// state. This spec is the class's end-to-end oracle: type into a doc, KILL
// the server mid-session, keep the typing on screen, RESTART the server on
// the same port, and prove the retry kick lands the save on the reborn
// server without any user action.
//
// The stock `serve` fixture can't revive its process, so this spec owns a
// restartable server: same seedHome, same binary, same flags — plus
// kill()/start() the test drives.



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
    this.token = served.token; // the same password, so the same token across restarts
  }
  // kill is the OUTAGE: SIGKILL, so no graceful shutdown, no goodbye on any
  // stream — the same shape as a dropped link or a crashed box. Waits for
  // the exit so the port is genuinely free for the restart.
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

  // Seed a saved baseline so the outage save is a real versioned edit, not
  // a first write.
  await gw.typeText('saved before the outage. ');
  await expect
    .poll(async () => gw.getTileContent(created.id), { timeout: 10_000 })
    .toContain('saved before the outage.');

  // THE OUTAGE: kill the server, then type. The debounced autosave fires
  // into a dead socket.
  await outage.kill();
  await gw.typeText('typed while the server was dead.');

  // The typing must STAY ON SCREEN and STAY DIRTY — before the class fix,
  // the failed save dropped the buffer (the only copy) and the next render
  // repainted the textarea from stale bytes.
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

  // And the failure is SURFACED, not silent (charter §6): some notice is on
  // the strip (save-retry, disconnect — the class talks; which voice is not
  // pinned here).
  await expect
    .poll(async () => {
      const errs = await window.evaluate(() => (window as any).__gridwellTest.errors());
      return errs.notices.length;
    })
    .toBeGreaterThan(0);

  // THE RECOVERY: same port, same home. No user action from here on — the
  // reconnect kick (SSE re-open) plus the flush sweep must land the dirty
  // buffer on the reborn server by themselves.
  await outage.start();
  await expect
    .poll(async () => gw.getTileContent(created.id), { timeout: 30_000 })
    .toContain('typed while the server was dead.');

  // The screen still shows the full document (no revert, no double-apply).
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

  // A FRESH well: its viewZoom stays 0 until the first framing write —
  // the same oracle framing-roundtrip.spec.ts pins settles with.
  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  const well = tileAt(await gw.getGrid(f.gridID), 'well', cx, cy)!;
  await gw.descendCell(cx, cy);
  const inside = await gw.focused();

  await outage.kill();

  // Pan inside the well: the settle persister fires SetFraming into the
  // dead socket; the pending ledger must park it (before the class fix
  // the optimistic patch survived on screen but nothing ever re-posted).
  await gw.panFocusedGrid(
    Math.round(inside.cx) + 1,
    Math.round(inside.cy),
    Math.round(inside.cx) - 1,
    Math.round(inside.cy),
  );

  await outage.start();

  // The parked framing write lands on the reborn server without any user
  // action: the well row's viewZoom leaves its never-written 0.
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
