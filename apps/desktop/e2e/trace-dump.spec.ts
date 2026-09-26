import { ElectronApplication, Page } from '@playwright/test';
import { test, expect } from './fixtures';
import * as path from 'node:path';
import { readDump, joinedRequests, bootOrigins, describe as describeDump } from './trace';

// The trace has two halves and only a dump puts them together: the node's own
// ring, and the records the wasm client posts through /trace. This spec runs
// the gesture a user has — right-click the bar circle, pick Dump logs — and
// reads the file off disk, because the join between the halves is kv.req and
// nothing else in the suite can see it.

// The native popup blocks under xvfb, so Menu.popup is intercepted and the
// menu stashed; the item's own click still settles main's promise. Same trick
// as e2e/theme.spec.ts.
async function pickFromNativeMenu(electronApp: ElectronApplication, label: string): Promise<void> {
  await electronApp.evaluate(({ Menu }) => {
    const g = globalThis as any;
    g.__gwChoiceOrigPopup = Menu.prototype.popup;
    g.__gwChoiceMenu = null;
    (Menu.prototype as any).popup = function (this: any) {
      g.__gwChoiceMenu = this;
      return undefined;
    };
  });
  try {
    await expect
      .poll(() => electronApp.evaluate(() => Boolean((globalThis as any).__gwChoiceMenu)), {
        timeout: 10_000,
      })
      .toBe(true);
    await electronApp.evaluate((_: unknown, wanted: string) => {
      const m = (globalThis as any).__gwChoiceMenu;
      const row = m.items.find((i: any) => i.label === wanted);
      if (!row) throw new Error(`no ${wanted} row; got ${m.items.map((i: any) => i.label).join(', ')}`);
      row.click();
    }, label);
  } finally {
    await electronApp.evaluate(({ Menu }) => {
      const g = globalThis as any;
      if (g.__gwChoiceOrigPopup) (Menu.prototype as any).popup = g.__gwChoiceOrigPopup;
      delete g.__gwChoiceOrigPopup;
      delete g.__gwChoiceMenu;
    });
  }
}

// The dump's path arrives as a notice, which is also how the user finds it.
async function dumpNotice(window: Page): Promise<{ severity: string; message: string }> {
  return expect
    .poll(
      async () => {
        const e = await window.evaluate(() => (window as any).__gridwellTest.errors());
        return e.notices.find((n: any) => n.source === 'trace') ?? null;
      },
      { timeout: 20_000 },
    )
    .not.toBeNull()
    .then(async () =>
      window.evaluate(
        () =>
          (window as any).__gridwellTest
            .errors()
            .notices.find((n: any) => n.source === 'trace'),
      ),
    );
}

test('Dump logs writes one file holding both halves of the trace', async ({
  electronApp,
  gw,
  home,
  window,
}) => {
  await gw.enterPlugin('home');
  // A gesture with server work behind it, so the join is on a call this spec
  // made rather than only on the boot's.
  const f = await gw.focused();
  await gw.openPalette();
  await gw.dragCreate('markdown', Math.round(f.cx), Math.round(f.cy));
  await gw.waitIdle();

  const cid: string = (await window.evaluate(() => (window as any).__gridwellTest.trace())).cid;
  expect(cid, 'the client mints a cid at boot').toBeTruthy();

  const popped = pickFromNativeMenu(electronApp, 'Dump logs');
  await gw.rightClickCircle();
  await popped;

  const notice = await dumpNotice(window);
  expect(notice.severity, 'a dump that worked is not an error').toBe('info');
  const named = /logs dumped to (\S+)/.exec(notice.message);
  expect(named, `the notice names the file: ${notice.message}`).toBeTruthy();
  const file = named![1];
  expect(file.startsWith(path.join(home, 'dumps')), `${file} is under this run's home`).toBe(true);

  const lines = readDump(file);
  const mine = lines.filter((r) => r.origin === 'client' && r.cid === cid);
  expect(mine.length, `no record carries this client's cid; ${describeDump(lines)}`).toBeGreaterThan(0);

  // The gesture is in the file, not only the plumbing: the press, the frame
  // the release asked for, and the verdict it took.
  const kinds = new Set(mine.map((r) => `${r.src}/${r.kind}`));
  for (const want of ['gesture/press', 'frame/draw', 'gesture/release', 'drag/drop']) {
    expect([...kinds], `the dump holds a ${want} record`).toContain(want);
  }

  // A paint has a reason, and the reason has to be something that happened.
  // The notice strip changes only when a notice goes up or comes down, so the
  // frames asked for under it cannot outnumber the notices in the same dump: a
  // read or write that simply worked, resolving a source that had no notice,
  // is not a reason to repaint.
  const noticeFrames = mine.filter(
    (r) => r.src === 'frame' && r.kind === 'draw' && r.msg === 'notice strip',
  );
  const notices = mine.filter((r) => r.kind === 'notice');
  expect(
    noticeFrames.length,
    `${noticeFrames.length} frames asked for by the notice strip, against ${notices.length} notices`,
  ).toBeLessThanOrEqual(notices.length);

  // The pane is the shim's to supply, and it is what joins the release to the
  // frames and writes around it.
  // A drawn frame is one record, and it says how long the draw took.
  const frame = mine.find((r) => r.src === 'frame' && r.kind === 'draw');
  expect(Number(frame?.kv?.ms), `the frame record carries its duration: ${JSON.stringify(frame)}`)
    .toBeGreaterThanOrEqual(0);
  expect(mine.some((r) => r.src === 'frame' && r.kind === 'schedule'), 'no frame is recorded twice')
    .toBe(false);

  const drop = mine.find((r) => r.src === 'drag' && r.kind === 'drop');
  expect(drop?.kv?.pane, `the drop record names the pane it was made in: ${JSON.stringify(drop)}`)
    .toBeTruthy();

  // Every origin names its build once, at its start.
  expect(bootOrigins(lines), `boot records; ${describeDump(lines)}`).toEqual(['client', 'electron', 'node']);
  expect(mine.filter((r) => r.kind === 'boot').length, 'this client boots once').toBe(1);

  const joined = joinedRequests(lines);
  expect(
    joined.length,
    `no request id names both a client and a node rpc record; ${describeDump(lines)}`,
  ).toBeGreaterThan(0);

  // Seq is the node's stamp and the one total order a reader follows.
  const seqs = lines.map((r) => r.seq ?? 0);
  expect(seqs, "the dump is written in the node's own order").toEqual(
    [...seqs].sort((a, b) => a - b),
  );
});
