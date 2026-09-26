import { test, expect } from './fixtures';
import * as path from 'node:path';
import { readDump, joinedRequests, bootOrigins, describe as describeDump } from '../e2e/trace';

// A phone has no native menu, so the circle's right-click draws its own DOM
// popover: this is the other renderer's half of e2e/trace-dump.spec.ts. The
// dump itself is the same door, and the file must hold both halves of the
// trace whichever menu ran the row.

test('the circle popover dumps the trace and names the file', async ({ gw, serve, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  await gw.openPalette();
  await gw.dragCreate('markdown', Math.round(f.cx), Math.round(f.cy));
  await gw.waitIdle();

  const cid: string = (await window.evaluate(() => (window as any).__gridwellTest.trace())).cid;
  expect(cid, 'the client mints a cid at boot').toBeTruthy();

  await gw.rightClickCircle();
  const popover = window.locator('#gw-circle-menu');
  await popover.waitFor({ timeout: 5_000 });
  // The dump names no state, so it wears no check: the popover marks only a
  // row that stands for something.
  expect(
    await popover.locator('[data-gw-choice="dump"]').textContent(),
    'the dump row is a plain action row',
  ).toBe('Dump logs');
  await popover.locator('[data-gw-choice="dump"]').click();
  await expect(popover).toHaveCount(0);

  const notice = await expect
    .poll(
      async () => {
        const e = await window.evaluate(() => (window as any).__gridwellTest.errors());
        return e.notices.find((n: any) => n.source === 'trace')?.message ?? null;
      },
      { timeout: 20_000 },
    )
    .not.toBeNull()
    .then(async () =>
      window.evaluate(
        () =>
          (window as any).__gridwellTest.errors().notices.find((n: any) => n.source === 'trace')
            .message as string,
      ),
    );
  const named = /logs dumped to (\S+)/.exec(notice);
  expect(named, `the notice names the file: ${notice}`).toBeTruthy();
  const file = named![1];
  expect(file.startsWith(path.join(serve.home, 'dumps')), `${file} is under this run's home`).toBe(
    true,
  );

  const lines = readDump(file);
  const mine = lines.filter((r) => r.origin === 'client' && r.cid === cid);
  expect(mine.length, `no record carries this client's cid; ${describeDump(lines)}`).toBeGreaterThan(0);
  // A browser has no main process, so two origins name their builds.
  expect(bootOrigins(lines), `boot records; ${describeDump(lines)}`).toEqual(['client', 'node']);
  expect(
    joinedRequests(lines).length,
    `no request id names both a client and a node rpc record; ${describeDump(lines)}`,
  ).toBeGreaterThan(0);
});
