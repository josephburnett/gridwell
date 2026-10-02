import { test, expect } from './fixtures';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { makeRunDir } from './homes';
import { settle } from './cadence';

// A directory of many directories, opened through the fs plugin, shows each as
// a well whose preview reads its child grid. That read is once, and nothing
// the source says afterwards may turn into a storm of re-reads: on 2026-10-02
// a home of ~100 directories made the client cancel and re-ask every grid on
// each health flip, and each cancelled read came back to the node as a flip.
//
// On a Mac the first flip is the change watcher: fsnotify there is kqueue,
// which holds one file descriptor per file in every watched directory, so the
// plugin runs out and refuses Watch with ResourceExhausted. Linux's inotify
// limit is far above what this spec shows, so the flip is injected the two
// other ways it starts: reads abandoned in flight, and the plugin process
// dying and being respawned.

const ROOT = makeRunDir();
const DIRS = 150;
for (let i = 0; i < DIRS; i++) {
  const d = path.join(ROOT, `d${String(i).padStart(3, '0')}`);
  fs.mkdirSync(d, { recursive: true });
  for (let j = 0; j < 20; j++) fs.writeFileSync(path.join(d, `f${j}.txt`), `dir ${i} file ${j}\n`);
}
test.use({ extraPlugins: [{ kind: 'fs', name: 'dirs', config: { root: ROOT } }] });

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

// watchNotices samples the notice strip for ms, returning every message seen.
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

// fsPluginPid is the fs plugin subprocess the node's sidecar spawned.
async function fsPluginPid(electronApp: any): Promise<number> {
  const sidecar: number = await electronApp.evaluate(
    () => (globalThis as { __gwSidecarPid?: number }).__gwSidecarPid ?? 0,
  );
  for (const ent of fs.readdirSync('/proc')) {
    if (!/^\d+$/.test(ent)) continue;
    try {
      const stat = fs.readFileSync(`/proc/${ent}/stat`, 'utf8');
      const ppid = Number(stat.slice(stat.lastIndexOf(')') + 2).split(' ')[1]);
      const cmd = fs.readFileSync(`/proc/${ent}/cmdline`, 'utf8');
      if (ppid === sidecar && cmd.includes('gridwell-plugin-fs')) return Number(ent);
    } catch {
      // gone between the listing and the read
    }
  }
  throw new Error(`no fs plugin under sidecar ${sidecar}`);
}

test('a directory of many directories reads each once and stays quiet', async ({ electronApp, gw, window }) => {
  test.setTimeout(180_000);
  const reads = await readsOf(window);
  const total = () => [...reads.values()].reduce((a, b) => a + b, 0);
  // Read straight off the hook: the list is empty while a handshake is in
  // flight, and that is "not yet", not a failure.
  const status = (): Promise<string> =>
    window.evaluate(
      () => (window as any).__gridwellTest.plugins().find((p: any) => p.label === 'dirs')?.status ?? 'unlisted',
    );

  await gw.enterPlugin('dirs');
  const here = await gw.focused();
  const wells = ((await gw.getGrid(here.gridID)).tiles ?? []).filter((t) => t.kind === 'well');
  expect(wells, 'every directory is a well').toHaveLength(DIRS);
  // Zoomed out until every well is on screen, as the trace's pane was.
  const previewed = () => wells.filter((w) => reads.has(w.childGridId!)).length;
  for (let i = 0; i < 16 && previewed() < DIRS; i++) {
    // Over an empty cell: a wheel over a well is not the grid's.
    const empty = await gw.cellCenter(here.id, -1, 0);
    await window.mouse.move(empty.x, empty.y);
    await window.mouse.wheel(0, 300);
    await gw.waitIdle();
  }
  expect(previewed(), 'every well on screen read its grid for a preview').toBe(DIRS);
  expect(await watchNotices(window, 2_000), 'opening many directories raises no notice').toEqual([]);
  expect(await status(), 'and the plugin stays healthy').toBe('');
  for (const w of wells) {
    expect(reads.get(w.childGridId!) ?? 0, `the preview of ${w.altText} read its grid`).toBeLessThanOrEqual(2);
  }

  // settledAfter holds the source to the end of its news: the client goes idle,
  // the health strip clears, and then nothing reads it at all.
  const settledAfter = async (what: string) => {
    await expect.poll(status, { timeout: 30_000, message: `${what}: the plugin is healthy again` }).toBe('');
    await gw.waitIdle();
    await watchNotices(window, 2_000);
    for (const w of wells) {
      expect(reads.get(w.childGridId!) ?? 0, `${what}: ${w.altText} was re-read a bounded number of times`).toBeLessThanOrEqual(4);
    }
    reads.clear();
    const quiet = await watchNotices(window, 3_000);
    expect(total(), `${what}: once the source is back, nothing keeps re-reading it`).toBe(0);
    expect(quiet, `${what}: and the strip is clear`).toEqual([]);
  };

  // Reads abandoned in flight: a reload while the boot is reading the wells'
  // grids cancels them at the node.
  await settle(window, (await gw.cadences()).framingSaveMs);
  await window.reload();
  await expect.poll(() => reads.size, { timeout: 30_000 }).toBeGreaterThan(20);
  reads.clear();
  await window.reload();
  await window.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
  await settledAfter('reads abandoned by a reload');

  // The source goes down and comes back once. Its two transitions each owe a
  // resync, and nothing more may follow.
  const killed = await fsPluginPid(electronApp);
  process.kill(killed, 'SIGKILL');
  await expect
    .poll(() => fsPluginPid(electronApp).catch(() => killed), { timeout: 30_000 })
    .not.toBe(killed);
  await settledAfter('a respawned plugin');
});
