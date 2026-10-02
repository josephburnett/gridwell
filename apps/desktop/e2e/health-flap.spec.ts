import { test, expect } from './fixtures';

// A health transition cancels and re-reads every grid its source serves, so
// a flapping source used to cost one resync per flip, and each cancelled read
// posted "grid unavailable: context canceled" (the 2026-10-02 storm). A flip
// now resyncs once its health has held for cadence.HealthSettleMs
// (events.Resyncs), and a read the client cancelled itself says nothing
// (clientsync.OutcomeAbandoned).
//
// The node's event door is the lever: the page's Subscribe is answered with
// twenty transitions of the home namespace and a clean end, and the reconnect
// after it is held, so no stream-gap resync muddies the count.

// envelope frames one Connect streaming message: a flag byte, a big-endian
// length, the JSON payload. Flag 2 is the end of the stream.
function envelope(flag: number, payload: object): Buffer {
  const body = Buffer.from(JSON.stringify(payload));
  const head = Buffer.alloc(5);
  head.writeUInt8(flag, 0);
  head.writeUInt32BE(body.length, 1);
  return Buffer.concat([head, body]);
}

test('twenty health flips in a second resync once and say nothing about grids', async ({ gw, window }) => {
  const cad = await gw.cadences();
  const home = (await gw.plugins()).find((p) => p.label === 'home')!;
  const landed = await gw.focused();
  expect(landed.gridID.startsWith(home.uuid + '/'), 'the home grid is served through the home source').toBe(true);

  let gate!: () => void;
  const opened = new Promise<void>((r) => (gate = r));
  let release!: () => void;
  const released = new Promise<void>((r) => (release = r));
  let subscribes = 0;
  const flips = Array.from({ length: 20 }, (_, i) =>
    envelope(0, { pluginHealth: { pluginUuid: home.uuid, healthy: i % 2 === 0, detail: `flap ${i}` } }),
  );
  await window.route('**/gridwell.v1.Gridwell/Subscribe', async (r: any) => {
    if (subscribes++ > 0) {
      await released;
      await r.continue().catch(() => {});
      return;
    }
    await opened;
    await r.fulfill({
      status: 200,
      headers: { 'Content-Type': 'application/connect+json' },
      body: Buffer.concat([...flips, envelope(2, {})]),
    });
  });

  const reads: string[] = [];
  await window.route('**/gridwell.v1.Gridwell/GetGrid', async (r: any) => {
    reads.push(String(JSON.parse(r.request().postData() ?? '{}').gridId ?? ''));
    await r.continue();
  });
  const said: string[] = [];
  window.on('console', (m: any) => {
    if (m.text().includes('grid unavailable')) said.push(m.text());
  });

  try {
    await window.reload();
    await window.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
    await expect.poll(async () => (await gw.focused()).gridID, { timeout: 30_000 }).toBe(landed.gridID);
    await gw.waitIdle();
    reads.length = 0;

    gate();
    await expect
      .poll(() => reads.filter((id) => id === landed.gridID).length, {
        message: 'the settled resync reads the home grid',
        timeout: 10_000,
      })
      .toBeGreaterThan(0);
    // Long enough for a second settle window to close had the burst armed two.
    await window.waitForTimeout(cad.healthSettleMs * 2);
    await gw.waitIdle();

    expect.soft(reads.filter((id) => id === landed.gridID).length, 'one resync for the whole burst').toBe(1);
    expect.soft(said, 'no cancelled read reached the strip').toEqual([]);
    const errs = await window.evaluate(() => (window as any).__gridwellTest.errors());
    const sources: string[] = errs.notices.map((n: any) => n.source);
    expect(sources.filter((s) => s.startsWith('grid:')), 'no grid notice stands').toEqual([]);
    expect(
      sources.filter((s) => s === 'plugin:' + home.uuid),
      'the burst ended down, so its one sticky notice is up',
    ).toHaveLength(1);
  } finally {
    release();
    await window.unroute('**/gridwell.v1.Gridwell/GetGrid');
    await window.unroute('**/gridwell.v1.Gridwell/Subscribe');
  }
});
