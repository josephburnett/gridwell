import { test, expect, homePassword, loginToken } from './fixtures';
import { tileAt } from './oracle';
import { dumpNow } from './trace';

// A source that declares no watch is complete as declared: the node says only
// that its changes are not told (EventPluginHealth.told), and the client shows
// nothing. A source that lists but fails to watch is not dark: the node says
// so as EventPluginHealth.live_updates_off on a healthy event, and the client
// shows one sticky notice of its own, apart from the dark one, and resyncs
// nothing (events.ReactHealth). The node side, a real refused Watch whose listings
// still answer, is internal/server/live_updates_off_seam_test.go: no OS lever
// makes the shipped fs plugin refuse a watch while it still lists, so the
// page's Subscribe is the lever here, one held stream per stage. Neither
// source is told, so a move onto its grid reads it. Each
// stage's stream ends, and a client whose stream is down reads on every move
// (cache.NoteStream), so "told again" is asked of the real stream opened last.

// envelope frames one Connect streaming message: a flag byte, a big-endian
// length, the JSON payload. Flag 2 is the end of the stream.
function envelope(flag: number, payload: object): Buffer {
  const body = Buffer.from(JSON.stringify(payload));
  const head = Buffer.alloc(5);
  head.writeUInt8(flag, 0);
  head.writeUInt32BE(body.length, 1);
  return Buffer.concat([head, body]);
}

test('a refused watch is one notice of its own, costs no resync, and clears when the watch opens; a declared no-watch says nothing', async ({ gw, home, window }) => {
  const cad = await gw.cadences();
  const homePlugin = (await gw.plugins()).find((p) => p.label === 'home')!;
  const landed = await gw.focused();
  const cx = Math.round(landed.cx);
  const cy = Math.round(landed.cy);
  await gw.openMenu();
  await gw.dragCreate('door', cx, cy);
  expect(tileAt(await gw.getGrid(landed.gridID), 'door', cx, cy), 'a door to move through').toBeTruthy();
  const roundTrip = async () => {
    await gw.descendCell(cx, cy);
    await gw.ascendViaCrumb();
    await gw.waitIdle();
  };
  const health = (h: object) => envelope(0, { pluginHealth: { pluginUuid: homePlugin.uuid, ...h } });
  const refused = 'the OS refused another change watch';

  // Each stage answers one Subscribe and ends it; the reconnect waits for the
  // next stage, so its stream-gap resync never lands inside a stage. The one
  // after the last waits on release and reaches the node.
  const stages = [
    [health({ healthy: true, liveUpdatesOff: refused, told: false })],
    [health({ healthy: false, detail: 'gone', liveUpdatesOff: refused, told: false })],
    [health({ healthy: true, liveUpdatesOff: refused, told: false })],
    [health({ healthy: true, told: true })],
    [health({ healthy: true, told: false })],
    [health({ healthy: true, told: true })],
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
  const live = 'live:' + homePlugin.uuid;
  const dark = 'plugin:' + homePlugin.uuid;

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
    expect.soft((await gw.focused()).cached, 'its grids are answers, not cache').toBeFalsy();
    expect.soft(reads.filter((id) => id === landed.gridID), 'a refused watch alone costs no resync').toEqual([]);
    await roundTrip();
    expect.soft(reads.filter((id) => id === landed.gridID).length, 'an ascent reads the grid nothing tells').toBeGreaterThan(0);

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

    // A declared no-watch is not told, and is no failure: no notice comes up.
    gates[4].open();
    // The client reconnecting is that stage's stream read to its end.
    await expect.poll(() => subscribes, { message: 'the declaration is told' }).toBeGreaterThan(5);
    await window.waitForTimeout(cad.healthSettleMs * 2);
    ns = await notices();
    expect.soft([...of(ns, live), ...of(ns, dark)], 'a declared no-watch is no failure').toEqual([]);

    gates[5].open();
    // Past the settle window the dark spell's recovery resync rides on.
    await window.waitForTimeout(cad.healthSettleMs * 2);
    await gw.waitIdle();
    reads.length = 0;
    release();
    const token = await loginToken(gw.origin, homePassword(home));
    const opens = async () => {
      const { cid, lines } = await dumpNow(window, gw.origin, token);
      return lines.filter((r) => r.origin === 'client' && r.cid === cid && r.src === 'events' && r.kind === 'open').length;
    };
    await expect.poll(opens, { message: 'the real stream opens', timeout: 20_000 }).toBe(stages.length + 1);
    await expect.poll(() => reads.includes(landed.gridID), { message: 'its stream-gap resync reads the held grid' }).toBe(true);
    await gw.waitIdle();
    reads.length = 0;
    await roundTrip();
    expect.soft(reads.filter((id) => id === landed.gridID), 'told again, an ascent reads nothing').toEqual([]);
  } finally {
    for (const g of gates) g.open();
    release();
    await window.unroute('**/gridwell.v1.Gridwell/GetGrid');
    await window.unroute('**/gridwell.v1.Gridwell/Subscribe');
  }
});
