import { test as base, expect } from '@playwright/test';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { spawnServe, stopServe, authenticate, freePort } from './fixtures';
import { seedHome } from '../e2e/fixtures';
import { GridwellDriver } from '../e2e/driver';

// A dangling doorway — a link into a plugin no longer in server.yaml — must
// surface its verdict once and latch, not refetch and re-report every frame
// the well is on screen. The shape is a real one: a pre-one-node home whose
// conversion dropped a plugin leaves exactly these wells behind.

base('a link into an unconfigured plugin reports once, not per frame', async ({ page }) => {
  const docs = fs.mkdtempSync(path.join(os.tmpdir(), 'gw-docs-'));
  fs.writeFileSync(path.join(docs, 'note.md'), 'hello');
  const home = seedHome([{ kind: 'fs', name: 'files', config: { root: docs } }]);

  // Boot 1: with the plugin. Drop a link to it on the home root.
  const served1 = await spawnServe(home, await freePort());
  try {
    await authenticate(page, served1);
    await page.goto(served1.origin + '/?e2e=1');
    await page.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
    const gw = new GridwellDriver(page, served1.origin);
    await gw.waitIdle();
    const f = await gw.focused();
    await gw.openPalette();
    await gw.dragPluginLink('files', Math.round(f.cx), Math.round(f.cy));
    await gw.waitIdle();
    const seeded: any = await gw.getGrid(f.gridID);
    const link = (seeded.tiles ?? []).find((t: any) => t.kind === 'well');
    expect(link, 'boot 1 dropped the plugin link well').toBeTruthy();
  } finally {
    await stopServe(served1.child);
  }

  // The conversion-shaped edit: the plugin leaves the config, the home and
  // its link tile stay. Serve minted ids into server.yaml on boot 1; keep
  // everything but the plugins list.
  const yaml = fs.readFileSync(path.join(home, 'server.yaml'), 'utf8');
  const idLine = yaml.split('\n').find((l) => l.startsWith('id:'));
  expect(idLine, 'boot 1 minted the node id').toBeTruthy();
  fs.writeFileSync(path.join(home, 'server.yaml'), idLine + '\n');

  // Boot 2: the well is a dangling doorway. Its verdict must surface once.
  const unavailable: string[] = [];
  page.on('console', (m) => {
    if (m.text().includes('grid unavailable')) unavailable.push(m.text());
  });
  const served2 = await spawnServe(home, await freePort());
  try {
    await authenticate(page, served2);
    await page.goto(served2.origin + '/?e2e=1');
    await page.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
    const gw = new GridwellDriver(page, served2.origin);
    await gw.waitIdle();
    // Let the render loop run with the dangling well on screen: without the
    // verdict latch this collects a report per frame.
    await page.waitForTimeout(3_000);
    expect(unavailable.length, 'the dangling doorway must report').toBeGreaterThan(0);
    expect(
      unavailable.length,
      `one verdict, one report — got ${unavailable.length} (a per-frame refetch loop)`,
    ).toBeLessThanOrEqual(2);
  } finally {
    await stopServe(served2.child);
    fs.rmSync(home, { recursive: true, force: true });
    fs.rmSync(docs, { recursive: true, force: true });
  }
});
