import { test as base, expect } from '@playwright/test';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { spawnServe, stopServe, authenticate, freePort } from './fixtures';
import { GridwellDriver } from '../e2e/driver';

// Throwaway repro over a captured copy of a real pre-upgrade home: descend
// into a preexisting pane tile (reported: zooms but never sets the view),
// then drag it to the trash (reported: snaps back). Headless browser mode —
// same wasm, no Electron window. Skips when the capture is absent.

const CAPTURE = '/tmp/gw-debug/gridwell.db';
const CACHE = '/tmp/gw-debug/cache.db';
const NODE = '52f8374fa356402c66e41b8097341b09';

// fakeGitlab is a drifting /api/v4/todos: every walk sees different bodies,
// so every revalidation finds drift and emits GridChanged — the storm shape a
// real GitLab produces against a stale cache.
function fakeGitlab(): Promise<{ url: string; close: () => void }> {
  const http = require('node:http');
  let n = 0;
  const srv = http.createServer((req: any, res: any) => {
    n++;
    const u = new URL(req.url, 'http://x');
    const page = Number(u.searchParams.get('page') || '1');
    const state = u.searchParams.get('state') || 'pending';
    const todos =
      page > 1
        ? []
        : [1, 2, 3].map((i) => ({
            id: 900000 + i,
            state,
            action_name: 'review_requested',
            target_type: 'MergeRequest',
            body: `drift ${n}`,
            created_at: `2026-08-2${i}T10:00:00Z`,
            target_url: `https://gitlab.example/g/p/-/merge_requests/${i}`,
            target: { iid: i, title: `mr ${i} drift ${n}` },
            author: { name: 'Ada' },
            project: { path_with_namespace: 'g/p' },
          }));
    res.setHeader('content-type', 'application/json');
    res.end(JSON.stringify(todos));
  });
  return new Promise((resolve) => {
    srv.listen(0, '127.0.0.1', () =>
      resolve({ url: `http://127.0.0.1:${srv.address().port}`, close: () => srv.close() }),
    );
  });
}

base('captured home: descend into preexisting pane tile, then trash it', async ({ page }) => {
  base.skip(!fs.existsSync(CAPTURE), 'no captured home at ' + CAPTURE);
  const home = fs.mkdtempSync(path.join(os.tmpdir(), 'gw-joe-'));
  fs.copyFileSync(CAPTURE, path.join(home, 'gridwell.db'));
  fs.writeFileSync(path.join(home, 'server.yaml'), `id: ${NODE}\n`);
  const served = await spawnServe(home, await freePort());
  try {
    await authenticate(page, served);
    await page.goto(served.origin + '/?e2e=1');
    await page.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
    const gw = new GridwellDriver(page, served.origin);
    await gw.waitIdle();

    // The captured framing need not show the target cell in this viewport:
    // zoom out until it does, then click.
    const descendZoomed = async (cx: number, cy: number) => {
      for (let i = 0; ; i++) {
        try {
          await gw.descendCell(cx, cy);
          return;
        } catch (e) {
          if (!String(e).includes('outside pane') || i >= 14) throw e;
          await gw.wheelAtFocusedCenter(240); // positive dy: zoom out
        }
      }
    };

    // Root grid 1 → well "CD" (-11,-11) → grid 26 → well "Deploy Drivers"
    // (12,12) → grid 73, which holds pane tile 1468 at (-12,-18), 12x11.
    await descendZoomed(-11, -11);
    await descendZoomed(12, 12);
    const f = await gw.focused();
    expect(f.gridID, 'landed in the grid holding the pane tile').toBe(`${NODE}/73`);

    await descendZoomed(-7, -13); // a center cell of the 12x11 pane tile
    const ws = await page.evaluate(() => (window as any).__gridwellTest.workspace());
    const errs = await page.evaluate(() => (window as any).__gridwellTest.errors());
    console.log('REPRO workspace after descend:', JSON.stringify(ws));
    console.log('REPRO error strip:', JSON.stringify(errs));
    expect(ws.depth, 'descending into the pane tile must enter the workspace').toBe(1);

    await gw.leaveWorkspace();
    await gw.waitIdle();
    await gw.deleteTileCell(-7, -13);
    const g: any = await gw.getGrid(`${NODE}/73`);
    const still = (g.tiles ?? g.Tiles ?? []).find((t: any) => String(t.id ?? t.ID) === `${NODE}/1468`);
    console.log('REPRO errors after delete:', JSON.stringify(await page.evaluate(() => (window as any).__gridwellTest.errors())));
    expect(still, 'the trashed pane tile must leave the grid').toBeFalsy();
  } finally {
    await stopServe(served.child);
    fs.rmSync(home, { recursive: true, force: true });
  }
});

base('captured home + stale cache + drifting gitlab: gestures survive the churn', async ({ page }) => {
  base.skip(!fs.existsSync(CAPTURE) || !fs.existsSync(CACHE), 'no captured home/cache');
  base.setTimeout(180_000);
  const gl = await fakeGitlab();
  const home = fs.mkdtempSync(path.join(os.tmpdir(), 'gw-joe-'));
  fs.copyFileSync(CAPTURE, path.join(home, 'gridwell.db'));
  fs.copyFileSync(CACHE, path.join(home, 'cache.db'));
  fs.writeFileSync(path.join(home, 'token'), 'x\n');
  fs.writeFileSync(
    path.join(home, 'server.yaml'),
    [
      `id: ${NODE}`,
      'plugins:',
      '    - id: ngkwanw',
      '      kind: gitlab',
      '      label: gitlab',
      '      config:',
      `        url: ${gl.url}`,
      `        token_file: ${path.join(home, 'token')}`,
      '        refresh: 1s',
      '',
    ].join('\n'),
  );
  const served = await spawnServe(home, await freePort());
  const consoleLines: string[] = [];
  page.on('console', (m) => {
    const t = m.text();
    if (t.includes('gridwell:')) consoleLines.push(t);
  });
  try {
    await authenticate(page, served);
    await page.goto(served.origin + '/?e2e=1');
    await page.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
    const gw = new GridwellDriver(page, served.origin);
    await gw.waitIdle();

    const descendZoomed = async (cx: number, cy: number) => {
      for (let i = 0; ; i++) {
        try {
          await gw.descendCell(cx, cy);
          return;
        } catch (e) {
          if (!String(e).includes('outside pane') || i >= 14) throw e;
          await gw.wheelAtFocusedCenter(240);
        }
      }
    };

    // Walk to grid 73 and exercise Joe's two gestures repeatedly across the
    // 30s freshness window, while the drifting fake keeps revalidations and
    // GridChanged flowing.
    await descendZoomed(-11, -11);
    await descendZoomed(12, 12);
    for (let round = 0; round < 3; round++) {
      await descendZoomed(-7, -13); // into the pane tile
      const ws = await page.evaluate(() => (window as any).__gridwellTest.workspace());
      console.log(`REPRO round ${round} descend depth=${ws.depth} tile=${ws.tileID}`);
      expect(ws.depth, `round ${round}: descend must enter the workspace`).toBe(1);
      await gw.leaveWorkspace();
      await gw.waitIdle();
      if (round < 2) await page.waitForTimeout(16_000); // cross the window "after a while"
    }
    // The trash gesture under churn.
    await gw.deleteTileCell(-7, -13);
    const g: any = await gw.getGrid(`${NODE}/73`);
    const still = (g.tiles ?? g.Tiles ?? []).find((t: any) => String(t.id ?? t.ID) === `${NODE}/1468`);
    console.log('REPRO errors:', JSON.stringify(await page.evaluate(() => (window as any).__gridwellTest.errors())));
    console.log('REPRO console:', JSON.stringify(consoleLines.slice(0, 20)));
    expect(still, 'the trashed pane tile must leave the grid').toBeFalsy();
  } finally {
    await stopServe(served.child);
    gl.close();
    fs.rmSync(home, { recursive: true, force: true });
  }
});
