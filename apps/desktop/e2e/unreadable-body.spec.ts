import { test, expect, homePassword, loginToken } from './fixtures';
import { envelope, tileAt, updateText } from './oracle';
import { dumpNow } from './trace';

// A body the server refuses is the same answer every time it is asked, so a
// tile on screen whose ReadContent fails asks once and says so once, until
// something changes the body. The renderer asks for a missing body on every
// draw and a notice draws a frame, so without a failure latch each refusal
// paints the frame that asks again.

// endStream is a Connect server stream ending in an error before any message:
// not_found as the node answers for a link whose fs file is gone, unavailable
// as it answers for a plugin that is down.
function endStream(code: string, message: string): Buffer {
  return envelope(0x02, Buffer.from(JSON.stringify({ error: { code, message } })));
}

test('a body the server refuses is asked for once, reported once, and asked again on a change', async ({
  gw,
  home,
  window,
}) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const tile = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;
  expect(tile, 'markdown tile created').toBeTruthy();
  await gw.waitIdle();

  let refuse = true;
  let refused = 0;
  let passed = 0;
  await window.route('**/gridwell.v1.Gridwell/ReadContent', (r: any) => {
    if (!(r.request().postDataBuffer() ?? Buffer.alloc(0)).includes(tile.id)) {
      return r.continue();
    }
    if (!refuse) {
      passed++;
      return r.continue();
    }
    refused++;
    return r.fulfill({ status: 200, contentType: 'application/connect+json', body: endStream('not_found', `plugin: no tile ${tile.id}`) });
  });

  // A reload empties the body cache, so the first draw of the tile asks.
  await window.reload();
  await window.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
  await expect.poll(async () => (await gw.focused()).gridID, { timeout: 30_000 }).toBe(f.gridID);
  await expect.poll(() => refused, { timeout: 10_000 }).toBeGreaterThan(0);

  // Two seconds of the tile on screen is over a hundred frames if each
  // refusal draws the next.
  await window.waitForTimeout(2_000);
  const asked = refused;
  const e = await window.evaluate(() => (window as any).__gridwellTest.errors());
  // Let reads through before the dump, because a client asking every frame
  // never drains its trace backlog.
  refuse = false;
  const { cid, lines } = await dumpNow(window, gw.origin, await loginToken(gw.origin, homePassword(home)));

  const mine = lines.filter((r) => r.origin === 'client' && r.cid === cid);
  const reads = mine.filter((r) => r.kind === 'rpc' && r.msg.startsWith('ReadContent error'));
  const notices = mine.filter((r) => r.kind === 'notice' && r.src === 'rpc:ReadContent');
  expect(
    { asked, tracedReads: reads.length, tracedNotices: notices.length },
    'ReadContent calls the route answered, client spans ending in the verdict, notices raised',
  ).toEqual({ asked: 1, tracedReads: 1, tracedNotices: 1 });
  expect(reads[0].kv?.code).toBe('not_found');
  expect(
    e.notices.filter((n: any) => n.source === 'rpc:ReadContent').length,
    'the strip holds one notice for it',
  ).toBe(1);

  // The latch is not a verdict forever: another writer changing the body is
  // the TileChanged that earns one more read, and that read lands.
  expect(passed, 'nothing re-asked the latched body on its own').toBe(0);
  await updateText(gw.origin, tile.id, Number(tile.version ?? 0), 'written elsewhere');
  await expect.poll(() => passed, { timeout: 10_000 }).toBe(1);
  await window.waitForTimeout(500);
  expect(passed, 'the body that landed is not asked for again').toBe(1);
  await window.unroute('**/gridwell.v1.Gridwell/ReadContent');
});

// An unreachable source is not a verdict, so its latch does not stand until a
// change: the backstop re-asks once per interval. Without a latch at all the
// renderer re-asks every frame.
test('a body whose source is unreachable is asked for once per backstop, not per frame', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const tile = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;
  expect(tile, 'markdown tile created').toBeTruthy();
  await gw.waitIdle();

  let down = true;
  let unanswered = 0;
  let passed = 0;
  await window.route('**/gridwell.v1.Gridwell/ReadContent', (r: any) => {
    if (!(r.request().postDataBuffer() ?? Buffer.alloc(0)).includes(tile.id)) {
      return r.continue();
    }
    if (!down) {
      passed++;
      return r.continue();
    }
    unanswered++;
    return r.fulfill({ status: 200, contentType: 'application/connect+json', body: endStream('unavailable', 'plugin down') });
  });

  await window.reload();
  await window.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
  await expect.poll(async () => (await gw.focused()).gridID, { timeout: 30_000 }).toBe(f.gridID);
  await expect.poll(() => unanswered, { timeout: 10_000 }).toBeGreaterThan(0);

  // Retuning restarts the wait, so every re-ask from here is a tick of this
  // interval: four seconds holds at most three.
  const backstopMs = 1_500;
  const start = unanswered;
  expect(
    await window.evaluate((ms: number) => (window as any).__gridwellTest.setBackstopMs(ms), backstopMs),
  ).toBe(backstopMs);
  await window.waitForTimeout(4_000);
  const reasked = unanswered - start;
  expect(reasked, `re-asks in 4s on a ${backstopMs}ms backstop`).toBeLessThanOrEqual(3);
  expect(reasked, 'the backstop re-asks an unreachable body').toBeGreaterThanOrEqual(1);

  // The source comes back, and the next tick's read lands and is the last.
  down = false;
  await expect.poll(() => passed, { timeout: 2 * backstopMs + 5_000 }).toBe(1);
  await window.waitForTimeout(2 * backstopMs);
  expect(passed, 'the body that landed is not asked for again').toBe(1);
  await window.unroute('**/gridwell.v1.Gridwell/ReadContent');
});
