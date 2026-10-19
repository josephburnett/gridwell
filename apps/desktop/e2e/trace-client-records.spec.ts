import { test, expect, homePassword, loginToken } from './fixtures';
import { envelope } from './oracle';
import { dumpNow, TraceLine } from './trace';
import { pickFromNativeMenu } from './choicemenu';

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

test('a notice pressed away leaves a record of how it went', async ({ electronApp, gw, home, window }) => {
  await gw.enterPlugin('home');
  await gw.waitIdle();
  // Dump logs puts up an info notice with no button, so a press on its row
  // dismisses it.
  const popped = pickFromNativeMenu(electronApp, 'Dump logs');
  await gw.rightClickCircle();
  await popped;
  await expect
    .poll(async () => (await window.evaluate(() => (window as any).__gridwellTest.errors())).notices.length, {
      timeout: 20_000,
    })
    .toBeGreaterThan(0);
  const h = await window.evaluate(() => (window as any).innerHeight as number);
  await window.mouse.click(10, h - 5);
  await expect
    .poll(async () => (await window.evaluate(() => (window as any).__gridwellTest.errors())).notices.length)
    .toBe(0);

  const token = await loginToken(gw.origin, homePassword(home));
  let cid = '';
  let lines: TraceLine[] = [];
  await expect
    .poll(
      async () => {
        ({ cid, lines } = await dumpNow(window, gw.origin, token));
        return of(lines, cid, 'trace', 'notice-clear').map((r) => r.kv?.how);
      },
      { timeout: 20_000 },
    )
    .toEqual(['dismiss']);
});

test('the pane tree records its split, zoom, swap and close', async ({ gw, home, window }) => {
  await gw.enterPlugin('home');
  await gw.waitIdle();
  const [first] = await gw.panes();
  await gw.splitFocusedPaneVertical();
  await expect.poll(async () => (await gw.panes()).length).toBe(2);
  const [left, right] = (await gw.panes()).slice().sort((a, b) => a.x - b.x);

  await gw.clickBarName('left');
  await gw.clickBarName('left');

  // A right-drag from a pane's inner third onto another pane swaps them.
  await gw.rightDragScreen(left.x + left.w / 2, left.y + left.h / 2, right.x + right.w / 2, right.y + right.h / 2);
  await gw.collapseLeftPane();
  await expect.poll(async () => (await gw.panes()).length).toBe(1);

  const token = await loginToken(gw.origin, homePassword(home));
  let cid = '';
  let lines: TraceLine[] = [];
  await expect
    .poll(
      async () => {
        ({ cid, lines } = await dumpNow(window, gw.origin, token));
        return of(lines, cid, 'pane', 'close').length;
      },
      { timeout: 20_000 },
    )
    .toBeGreaterThan(0);

  const split = of(lines, cid, 'pane', 'split')[0];
  expect(split?.kv?.from, `the split names the pane it came from: ${JSON.stringify(split)}`).toBe(first.id);
  expect([left.id, right.id]).toContain(split.kv?.pane);
  expect(of(lines, cid, 'pane', 'zoom').map((r) => r.msg)).toEqual(['zoom', 'unzoom']);
  const swap = of(lines, cid, 'pane', 'swap')[0];
  expect(swap?.kv, `the swap names both panes: ${JSON.stringify(swap)}`).toEqual({ pane: right.id, from: left.id });
  const close = of(lines, cid, 'pane', 'close')[0];
  expect([left.id, right.id]).toContain(close.kv?.pane);
});

test("a reload sends the page's last records by beacon, its unload among them", async ({ gw, home, window }) => {
  await gw.enterPlugin('home');
  await gw.waitIdle();
  const before: string = (await window.evaluate(() => (window as any).__gridwellTest.trace())).cid;
  await window.reload();
  await window.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
  await gw.waitIdle();

  const token = await loginToken(gw.origin, homePassword(home));
  await expect
    .poll(
      async () => {
        const { lines } = await dumpNow(window, gw.origin, token);
        return of(lines, before, 'client', 'unload').length;
      },
      { timeout: 20_000 },
    )
    .toBe(1);
});

test('a drop names how many tiles it carried and the cell it landed on', async ({ gw, home, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  await gw.openMenu();
  await gw.dragCreate('markdown', cx, cy);
  await gw.waitIdle();
  await gw.dragTileCell(cx, cy, cx + 3, cy + 2);
  await gw.waitIdle();

  const token = await loginToken(gw.origin, homePassword(home));
  let cid = '';
  let lines: TraceLine[] = [];
  await expect
    .poll(
      async () => {
        ({ cid, lines } = await dumpNow(window, gw.origin, token));
        return of(lines, cid, 'drag', 'drop').filter((r) => r.msg === 'move').length;
      },
      { timeout: 20_000 },
    )
    .toBeGreaterThan(0);
  const move = of(lines, cid, 'drag', 'drop').find((r) => r.msg === 'move')!;
  expect(move.kv, JSON.stringify(move)).toMatchObject({ n: '1', x: String(cx + 3), y: String(cy + 2) });
});

test('a move the node never heard is taken back, and the dump says so', async ({ gw, home, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  await gw.openMenu();
  await gw.dragCreate('markdown', cx, cy);
  await gw.waitIdle();
  await window.route('**/gridwell.v1.Gridwell/MoveTile', (r: any) => r.abort());
  await gw.dragTileCell(cx, cy, cx + 1, cy);
  await gw.waitIdle();
  await window.unroute('**/gridwell.v1.Gridwell/MoveTile');

  const token = await loginToken(gw.origin, homePassword(home));
  let cid = '';
  let lines: TraceLine[] = [];
  await expect
    .poll(
      async () => {
        ({ cid, lines } = await dumpNow(window, gw.origin, token));
        return of(lines, cid, 'outbox', 'rollback').map((r) => r.kv);
      },
      { timeout: 20_000 },
    )
    .toEqual([{ op: 'MoveTile', grid: f.gridID, outcome: 'transport' }]);
});
