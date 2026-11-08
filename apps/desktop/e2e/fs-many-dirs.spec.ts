import { test, expect } from './fixtures';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { makeRunDir } from './homes';
import { settle } from './cadence';

// A directory of many directories shows each as a door whose preview reads its
// child grid. That read is once, and nothing the source says afterwards may
// turn into a storm of re-reads: on 2026-10-02 a home of ~100 directories made
// the client cancel and re-ask every grid on each health flip, and each
// cancelled read came back to the node as a flip. The flip is injected the way
// a client starts one: reads abandoned in flight by a reload.

const ROOT = makeRunDir();
const DIRS = 150;
for (let i = 0; i < DIRS; i++) {
  const d = path.join(ROOT, `d${String(i).padStart(3, '0')}`);
  fs.mkdirSync(d, { recursive: true });
  for (let j = 0; j < 20; j++) fs.writeFileSync(path.join(d, `f${j}.txt`), `dir ${i} file ${j}\n`);
}
test.use({ filesRoots: [{ root: ROOT, label: 'dirs' }] });

// readsOf counts GetGrid by grid id from now on, across reloads.
async function readsOf(window: any): Promise<Map<string, number>> {
  const reads = new Map<string, number>();
  await window.route('**/gridwell.v1.Gridwell/GetGrid', async (r: any) => {
    const id = String(JSON.parse(r.request().postData() ?? '{}').gridId ?? '');
    reads.set(id, (reads.get(id) ?? 0) + 1);
    await r.continue().catch(() => {});
  });
  return reads;
}

// watchNotices samples the notice row for ms, returning every message seen.
async function watchNotices(window: any, ms: number): Promise<string[]> {
  const seen = new Set<string>();
  const end = Date.now() + ms;
  while (Date.now() < end) {
    const e = await window.evaluate(() => (window as any).__gridwellTest.errors());
    for (const n of e.notices) seen.add(`${n.source}: ${n.message}`);
    await window.waitForTimeout(50);
  }
  return [...seen];
}

test('a directory of many directories reads each once and stays quiet', async ({ gw, window }) => {
  test.setTimeout(180_000);
  const reads = await readsOf(window);
  const total = () => [...reads.values()].reduce((a, b) => a + b, 0);
  // The files are healthy while the pane is not served from what it holds
  // (pane.cached, client/cache.SourceDark).
  const dark = async (): Promise<boolean> => !!(await gw.focused()).cached;

  await gw.enterSource('dirs (files)');
  const here = await gw.focused();
  const doors = ((await gw.getGrid(here.gridID)).tiles ?? []).filter((t) => t.kind === 'door');
  expect(doors, 'every directory is a door').toHaveLength(DIRS);
  // Zoomed out until every door is on screen, as the trace's pane was.
  const previewed = () => doors.filter((w) => reads.has(w.childGridId!)).length;
  for (let i = 0; i < 16 && previewed() < DIRS; i++) {
    // Over an empty cell: a wheel over a door is not the grid's.
    const empty = await gw.cellCenter(here.id, -1, 0);
    await window.mouse.move(empty.x, empty.y);
    await window.mouse.wheel(0, 300);
    await gw.waitIdle();
  }
  expect(previewed(), 'every door on screen read its grid for a preview').toBe(DIRS);
  expect(await watchNotices(window, 2_000), 'opening many directories raises no notice').toEqual([]);
  expect(await dark(), 'and the files stay healthy').toBe(false);
  for (const w of doors) {
    expect(reads.get(w.childGridId!) ?? 0, `the preview of ${w.altText} read its grid`).toBeLessThanOrEqual(2);
  }

  // settledAfter holds the source to the end of its news: the client goes idle,
  // the notice row clears, and then nothing reads it at all.
  const settledAfter = async (what: string) => {
    await expect.poll(dark, { timeout: 30_000, message: `${what}: the files are healthy again` }).toBe(false);
    await gw.waitIdle();
    await watchNotices(window, 2_000);
    for (const w of doors) {
      expect(reads.get(w.childGridId!) ?? 0, `${what}: ${w.altText} was re-read a bounded number of times`).toBeLessThanOrEqual(4);
    }
    reads.clear();
    const quiet = await watchNotices(window, 3_000);
    expect(total(), `${what}: once the source is back, nothing keeps re-reading it`).toBe(0);
    expect(quiet, `${what}: and the notice row is clear`).toEqual([]);
  };

  // Reads abandoned in flight: a reload while the boot is reading the doors'
  // grids cancels them at the node.
  await settle(window, (await gw.cadences()).framingSaveMs);
  await window.reload();
  await expect.poll(() => reads.size, { timeout: 30_000 }).toBeGreaterThan(20);
  reads.clear();
  await window.reload();
  await window.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
  await settledAfter('reads abandoned by a reload');
});
