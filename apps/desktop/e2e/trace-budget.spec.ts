import { test, expect, homePassword, loginToken } from './fixtures';
import * as fs from 'node:fs';
import * as path from 'node:path';
import type { Page } from '@playwright/test';
import type { GridwellDriver } from './driver';
import { makeRunDir } from './homes';
import { dumpNow } from './trace';
import { attribute, budgetVerdict, GestureWindow, unattributed } from './tracebudget';

// A fixed script of ordinary use, then a quiet wait, then the ring read back:
// every node rpc and every source-layer work record must be owed to a
// gesture, an event, a view or a decision (tracebudget.ts). The count of
// those owed to none is held to trace-budget.json, which the trace-budget row
// of test/boundary/ratchet.txt reads, so this spec is what keeps that number
// true. The script reads only through the client's hook, never the oracle,
// whose calls carry no request id and would read as work for nobody.

const ROOT = makeRunDir();
const BUDGET = path.join(__dirname, 'trace-budget.json');
const QUIET_MS = 10_000;

test.use({
  extraPlugins: async ({}, use) => {
    await use([
      { kind: 'fs', name: 'code', config: { root: ROOT } },
      { kind: 'proc', name: 'procs', config: { pid: String(process.pid) } },
    ]);
  },
});

async function tileAt(window: Page, gw: GridwellDriver, cx: number, cy: number): Promise<string> {
  const f = await gw.focused();
  return window.evaluate(([id, x, y]) => (window as any).__gridwellTest.tileAt(id, x, y), [f.id, cx, cy] as [string, number, number]);
}

async function anchored(window: Page): Promise<void> {
  await window.waitForFunction(() => (window as any).__gridwellTest !== undefined, null, { timeout: 45_000 });
  await window.waitForFunction(
    () => {
      try {
        return ((window as any).__gridwellTest.panes() as Array<{ focused: boolean; anchor: string }>).some(
          (p) => p.focused && p.anchor !== '',
        );
      } catch {
        return false;
      }
    },
    null,
    { timeout: 45_000 },
  );
}

test('ordinary use does no work nobody asked for', async ({ gw, home, window }, testInfo) => {
  test.setTimeout(180_000);
  fs.writeFileSync(path.join(ROOT, 'one.txt'), 'one\n');
  const token = await loginToken(gw.origin, homePassword(home));
  const c = await gw.cadences();
  // A gesture's debounced writes land within the longest save wait after it.
  const settleMs = 2 * Math.max(c.textSaveMs, c.framingSaveMs, c.workspaceSaveMs, c.healthSettleMs);

  // The app booted before the body ran, so the boot gesture starts at zero.
  const windows: GestureWindow[] = [];
  let start = 0;
  const step = async (name: string, act: () => Promise<void>) => {
    await act();
    await gw.waitIdle();
    await window.waitForTimeout(settleMs);
    windows.push({ name, start, end: Date.now() });
    start = Date.now();
  };

  await step('boot', () => gw.enterPlugin('home'));
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  await step('text', async () => {
    await gw.openMenu();
    await gw.dragCreate('markdown', cx - 1, cy);
    await expect.poll(() => tileAt(window, gw, cx - 1, cy)).not.toBe('');
    await gw.descendCell(cx - 1, cy);
    await gw.typeText('trace budget');
    await gw.ascendViaCrumb();
  });

  await step('door', async () => {
    await gw.openMenu();
    await gw.dragCreate('door', cx + 1, cy);
    await expect.poll(() => tileAt(window, gw, cx + 1, cy)).not.toBe('');
    await gw.descendCell(cx + 1, cy);
    const inner = await gw.focused();
    const ix = Math.round(inner.cx);
    const iy = Math.round(inner.cy);
    await gw.panFocusedGrid(ix, iy, ix + 2, iy + 1);
    await gw.ascendViaCrumb();
  });

  await step('plugin', async () => {
    await gw.enterPlugin('code');
    await gw.ascendViaCrumb();
  });

  await step('copy', async () => {
    await gw.copyTileCell(cx - 1, cy, cx - 1, cy + 1);
    await expect.poll(() => tileAt(window, gw, cx - 1, cy + 1)).not.toBe('');
  });

  await step('delete', async () => {
    await gw.deleteTileCell(cx - 1, cy + 1);
    await expect.poll(() => tileAt(window, gw, cx - 1, cy + 1)).toBe('');
  });

  await step('reload', async () => {
    await window.evaluate(() => location.reload());
    await anchored(window);
  });

  await window.waitForTimeout(QUIET_MS);

  const { lines } = await dumpNow(window, gw.origin, token);
  const verdicts = attribute(lines, windows);
  const left = unattributed(verdicts);
  const byCause: Record<string, number> = {};
  for (const v of verdicts) byCause[v.cause ?? 'unattributed'] = (byCause[v.cause ?? 'unattributed'] ?? 0) + 1;
  console.log(`trace budget: ${verdicts.length} records audited ${JSON.stringify(byCause)}`);
  for (const u of left) console.log(`unattributed seq=${u.seq} ${u.src}/${u.kind} ${u.msg} ${u.id}`);
  await testInfo.attach('unattributed.json', {
    body: JSON.stringify({ windows, byCause, unattributed: left }, null, 2),
    contentType: 'application/json',
  });

  const recorded = (JSON.parse(fs.readFileSync(BUDGET, 'utf8')) as { unattributed: number }).unattributed;
  expect(budgetVerdict(left.length, recorded)).toBeNull();
});
