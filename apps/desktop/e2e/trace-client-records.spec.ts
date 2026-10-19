import { test, expect, homePassword, loginToken } from './fixtures';
import { envelope } from './oracle';
import { dumpNow, TraceLine } from './trace';

// The client's records that only a dump joins to the node's: each one says
// something the wire cannot, so each is read back through /trace from a real
// page rather than from client/traceevent alone.

function of(lines: TraceLine[], cid: string, src: string, kind: string): TraceLine[] {
  return lines.filter((r) => r.origin === 'client' && r.cid === cid && r.src === src && r.kind === kind);
}

test('a stream that ends and reopens leaves its open, its end and the resync it owed', async ({
  gw,
  home,
  window,
}) => {
  await gw.enterPlugin('home');
  // The page's first stream ends cleanly before any event, as a node that
  // closed it would; the next one goes through.
  let ended = 0;
  await window.route('**/gridwell.v1.Gridwell/Subscribe', (r: any) => {
    if (ended > 0) return r.continue();
    ended++;
    return r.fulfill({
      status: 200,
      contentType: 'application/connect+json',
      body: envelope(0x02, Buffer.from('{}')),
    });
  });
  await window.reload();
  await window.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
  await gw.waitIdle();

  const token = await loginToken(gw.origin, homePassword(home));
  let cid = '';
  let lines: TraceLine[] = [];
  await expect
    .poll(
      async () => {
        ({ cid, lines } = await dumpNow(window, gw.origin, token));
        return of(lines, cid, 'events', 'resync').length;
      },
      { timeout: 20_000 },
    )
    .toBeGreaterThan(0);

  const opens = of(lines, cid, 'events', 'open').map((r) => r.kv?.resync);
  expect(opens.slice(0, 2), 'the first stream owes nothing, the one after the gap owes the resync').toEqual([
    'false',
    'true',
  ]);
  const ends = of(lines, cid, 'events', 'end');
  expect(ends[0]?.kv?.reason, `the clean end is recorded: ${JSON.stringify(ends)}`).toBe('clean');
  const resync = of(lines, cid, 'events', 'resync')[0];
  expect(resync.kv?.source).toBe('every');
  expect(Number(resync.kv?.n), 'the resync counts the grids it re-reads').toBeGreaterThan(0);
});

test("the id on the client's records is the session the node files its view under", async ({
  gw,
  home,
  window,
}) => {
  await gw.enterPlugin('home');
  await gw.waitIdle();
  const token = await loginToken(gw.origin, homePassword(home));
  let cid = '';
  let lines: TraceLine[] = [];
  await expect
    .poll(
      async () => {
        ({ cid, lines } = await dumpNow(window, gw.origin, token));
        return lines.filter((r) => r.origin === 'node' && r.src === 'view' && r.kind === 'set' && r.kv?.session === cid)
          .length;
      },
      { timeout: 20_000 },
    )
    .toBeGreaterThan(0);
  expect(cid).toMatch(/^[a-z][a-z0-9]{6}$/);
});

test('both halves of an rpc span name the entity the request is about', async ({ gw, home, window }) => {
  await gw.enterPlugin('home');
  await gw.waitIdle();
  const token = await loginToken(gw.origin, homePassword(home));
  const { cid, lines } = await dumpNow(window, gw.origin, token);
  const mine = lines.filter(
    (r) => r.origin === 'client' && r.cid === cid && r.kind === 'rpc' && r.msg.startsWith('GetGrid start'),
  );
  expect(mine.length, 'the client read a grid').toBeGreaterThan(0);
  for (const c of mine) {
    expect(c.kv?.id, `the client's span names its grid: ${JSON.stringify(c)}`).toBeTruthy();
    const node = lines.find(
      (r) => r.origin === 'node' && r.kind === 'rpc' && r.kv?.req === c.kv?.req && r.msg.startsWith('GetGrid start'),
    );
    if (node) expect(node.kv?.id, 'one spelling on both sides').toBe(c.kv?.id);
  }
});
