import { test, expect } from './fixtures';

// A source that lists but cannot watch is not dark: the node says so as
// EventPluginHealth.live_updates_off on a healthy event, and the client shows
// one sticky notice of its own, apart from the dark one, and resyncs nothing
// (events.ReactHealth). The node side, a real refused Watch whose listings
// still answer, is internal/server/live_updates_off_seam_test.go: no OS lever
// makes the shipped fs plugin refuse a watch while it still lists, so the
// page's Subscribe is the lever here, one held stream per stage.

// envelope frames one Connect streaming message: a flag byte, a big-endian
// length, the JSON payload. Flag 2 is the end of the stream.
function envelope(flag: number, payload: object): Buffer {
  const body = Buffer.from(JSON.stringify(payload));
  const head = Buffer.alloc(5);
  head.writeUInt8(flag, 0);
  head.writeUInt32BE(body.length, 1);
  return Buffer.concat([head, body]);
}

test('a refused watch is one notice of its own, costs no resync, and clears when the watch opens', async ({ gw, window }) => {
  const cad = await gw.cadences();
  const home = (await gw.plugins()).find((p) => p.label === 'home')!;
  const landed = await gw.focused();
  const health = (h: object) => envelope(0, { pluginHealth: { pluginUuid: home.uuid, ...h } });
  const refused = 'the OS refused another change watch';

  // Each stage answers one Subscribe and ends it; the reconnect waits for the
  // next stage, so its stream-gap resync never lands inside a stage.
  const stages = [
    [health({ healthy: true, liveUpdatesOff: refused })],
    [health({ healthy: false, detail: 'gone', liveUpdatesOff: refused })],
    [health({ healthy: true, liveUpdatesOff: refused })],
    [health({ healthy: true })],
  ];
  const gates = stages.map(() => {
    let open!: () => void;
    const opened = new Promise<void>((r) => (open = r));
    return { open, opened };
  });
  let release!: () => void;
  const released = new Promise<void>((r) => (release = r));
  let subscribes = 0;
  await window.route('**/gridwell.v1.Gridwell/Subscribe', async (r: any) => {
    const i = subscribes++;
    if (i >= stages.length) {
      await released;
      await r.continue().catch(() => {});
      return;
    }
    await gates[i].opened;
    await r.fulfill({
      status: 200,
      headers: { 'Content-Type': 'application/connect+json' },
      body: Buffer.concat([...stages[i], envelope(2, {})]),
    });
  });
  const reads: string[] = [];
  await window.route('**/gridwell.v1.Gridwell/GetGrid', async (r: any) => {
    reads.push(String(JSON.parse(r.request().postData() ?? '{}').gridId ?? ''));
    await r.continue();
  });
  type Notice = { source: string; message: string; count: number };
  const notices = async (): Promise<Notice[]> => {
    const errs = await window.evaluate(() => (window as any).__gridwellTest.errors());
    return errs.notices;
  };
  const of = (ns: Notice[], key: string) => ns.filter((n) => n.source === key);
  const live = 'live:' + home.uuid;
  const dark = 'plugin:' + home.uuid;

  try {
    await window.reload();
    await window.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
    await expect.poll(async () => (await gw.focused()).gridID, { timeout: 30_000 }).toBe(landed.gridID);
    await gw.waitIdle();
    reads.length = 0;

    gates[0].open();
    await expect.poll(async () => of(await notices(), live).length, { message: 'the refusal is told' }).toBe(1);
    // Long enough for a settle window to close had the field armed one.
    await window.waitForTimeout(cad.healthSettleMs * 2);
    await gw.waitIdle();
    let ns = await notices();
    expect.soft(of(ns, live)[0]?.message).toBe(`home: live updates off — ${refused}`);
    expect.soft(of(ns, live)[0]?.count, 'told once').toBe(1);
    expect.soft(of(ns, dark), 'a source that lists is not dark').toEqual([]);
    expect.soft((await gw.focused()).stale, 'its rooms are answers, not memories').toBeFalsy();
    expect.soft(reads.filter((id) => id === landed.gridID), 'a refused watch alone costs no resync').toEqual([]);

    gates[1].open();
    await expect.poll(async () => of(await notices(), dark).length, { message: 'going dark is its own notice' }).toBe(1);
    expect.soft(of(await notices(), live), 'beside the refusal').toHaveLength(1);

    gates[2].open();
    await expect.poll(async () => of(await notices(), dark).length, { message: 'coming back takes it down' }).toBe(0);
    await gw.waitIdle();
    ns = await notices();
    expect.soft(of(ns, live), 'the refusal outlives the dark spell').toHaveLength(1);

    gates[3].open();
    await expect
      .poll(async () => of(await notices(), live).length, { message: 'the watch opening takes the notice down' })
      .toBe(0);
  } finally {
    for (const g of gates) g.open();
    release();
    await window.unroute('**/gridwell.v1.Gridwell/GetGrid');
    await window.unroute('**/gridwell.v1.Gridwell/Subscribe');
  }
});
