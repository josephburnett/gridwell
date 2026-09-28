import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { WebContentsView } from 'electron';
import { captureAttempt, capturable, CAPTURE_TIMEOUT_MS, MirrorPump, MIRROR_INTERVAL_MS } from './capture';

// CAPTURE_TIMEOUT_MS is a promise about time — the freeze path detaches only
// after capturePage settles — so these wait on it rather than on a number.

// A view whose capturePage settles after ms, or never.
function viewSettlingAfter(ms: number | null): WebContentsView {
  const image = { isEmpty: () => false, toJPEG: () => Buffer.from('jpeg-bytes') };
  return {
    webContents: {
      capturePage: () =>
        new Promise((resolve) => {
          if (ms !== null) setTimeout(() => resolve(image), ms);
        }),
    },
  } as unknown as WebContentsView;
}

test('a parked renderer times out at the declared bound', async () => {
  const started = Date.now();
  // The race is the failure mode: with no timeout the promise never settles,
  // and a hung test says less than a failed one.
  const attempt = await Promise.race([
    captureAttempt(viewSettlingAfter(null)),
    new Promise<'hung'>((r) => setTimeout(() => r('hung'), CAPTURE_TIMEOUT_MS * 4)),
  ]);
  assert.notEqual(attempt, 'hung', `capturePage never settled and captureAttempt waited past ${CAPTURE_TIMEOUT_MS * 4}ms`);
  assert.deepEqual(attempt, { kind: 'timeout', timeoutMs: CAPTURE_TIMEOUT_MS });
  // Timers may fire a hair early; the point is that the wait was the bound and
  // not something shorter.
  assert.ok(Date.now() - started >= CAPTURE_TIMEOUT_MS - 50, `gave up after ${Date.now() - started}ms, before the ${CAPTURE_TIMEOUT_MS}ms bound`);
});

test('a slow renderer that answers inside the bound still yields its frame', async () => {
  const attempt = await captureAttempt(viewSettlingAfter(CAPTURE_TIMEOUT_MS / 4));
  assert.equal(attempt.kind, 'ok');
});

// A pump whose timer is a seam, so a test can read the delay it scheduled and
// fire the rounds itself. Each timer is its own handle.
function pumpHarness(tick: (paneIds: readonly string[]) => Promise<void> = async () => {}) {
  const scheduled: number[] = [];
  const pending = new Map<number, () => void>();
  let next = 0;
  const pump = new MirrorPump(tick, {
    setTimer: (fn, ms) => {
      scheduled.push(ms);
      const id = ++next;
      pending.set(id, fn);
      return id;
    },
    clearTimer: (t) => {
      pending.delete(t as number);
    },
  });
  // A round's tail runs off its own promise.
  const settle = () => new Promise<void>((r) => setImmediate(r));
  // Fires every timer due now, as the event loop would.
  const fire = async () => {
    const due = [...pending.entries()];
    pending.clear();
    for (const [, fn] of due) fn();
    await settle();
  };
  return { pump, scheduled, armed: () => pending.size, fire, settle };
}

test('a pump nobody names a pane to arms no timer', () => {
  const h = pumpHarness();
  assert.equal(h.scheduled.length, 0);
  h.pump.setPanes([]);
  assert.equal(h.scheduled.length, 0, 'an empty set is not a reason to wake');
});

test('a named set is captured, exactly, at the declared cadence', async () => {
  const rounds: string[][] = [];
  const h = pumpHarness(async (ids) => {
    rounds.push([...ids]);
  });
  h.pump.setPanes(['p1', 'p3']);
  assert.deepEqual(h.scheduled, [MIRROR_INTERVAL_MS], 'the first round waits the cadence');
  await h.fire();
  await h.fire();
  assert.deepEqual(rounds, [['p1', 'p3'], ['p1', 'p3']]);
  assert.deepEqual(
    h.scheduled,
    [MIRROR_INTERVAL_MS, MIRROR_INTERVAL_MS, MIRROR_INTERVAL_MS],
    'a round that finished must not pull the next one in',
  );
});

test('a changed set lands on the next round without a second timer', async () => {
  const rounds: string[][] = [];
  const h = pumpHarness(async (ids) => {
    rounds.push([...ids]);
  });
  h.pump.setPanes(['p1']);
  h.pump.setPanes(['p1', 'p2']);
  assert.equal(h.armed(), 1, 'one pump, one timer');
  await h.fire();
  assert.deepEqual(rounds, [['p1', 'p2']]);
});

test('an emptied set stops the pump', async () => {
  let rounds = 0;
  const h = pumpHarness(async () => {
    rounds += 1;
  });
  h.pump.setPanes(['p1']);
  await h.fire();
  h.pump.setPanes([]);
  assert.equal(h.armed(), 0, 'no timer is left armed');
  await h.fire();
  assert.equal(rounds, 1, 'nothing is captured once nobody reads a face');
});

test('a stop and a restart during a round leave one pump running', async () => {
  let release: () => void = () => {};
  let rounds = 0;
  const h = pumpHarness(
    () =>
      new Promise<void>((r) => {
        rounds += 1;
        release = r;
      }),
  );
  h.pump.setPanes(['p1']);
  await h.fire(); // the round is now in flight
  h.pump.setPanes([]);
  h.pump.setPanes(['p2']);
  release();
  await h.settle();
  assert.equal(h.armed(), 1, 'the finished round must not chain a second timer beside the restart');
  assert.equal(rounds, 1);
});

test('a round that throws does not end the pump', async () => {
  let ticks = 0;
  const h = pumpHarness(async () => {
    ticks += 1;
    throw new Error('capturePage blew up');
  });
  h.pump.setPanes(['p1']);
  await h.fire();
  assert.equal(ticks, 1);
  assert.equal(h.armed(), 1, 'a failed round still schedules the next');
  await h.fire();
  assert.equal(ticks, 2, 'the pump keeps capturing after a failure');
});

test('the default timer waits the declared cadence', async () => {
  const at: number[] = [];
  const started = Date.now();
  const pump = new MirrorPump(async () => {
    at.push(Date.now() - started);
  });
  pump.setPanes(['p1']);
  await new Promise<void>((r) => setTimeout(r, MIRROR_INTERVAL_MS * 2 + MIRROR_INTERVAL_MS / 2));
  pump.stop();
  assert.ok(at.length >= 2, `expected two rounds in ${MIRROR_INTERVAL_MS * 2.5}ms, got ${at.length}`);
  // Node fires a timer a millisecond or two early and a loaded box fires it
  // late; the claim under test is that the pump waits the cadence and not
  // something shorter, so only the early side is bounded, at 5% of one round.
  const slack = MIRROR_INTERVAL_MS / 20;
  assert.ok(at[0] >= MIRROR_INTERVAL_MS - slack, `first round at ${at[0]}ms, before the ${MIRROR_INTERVAL_MS}ms cadence`);
  assert.ok(at[1] >= MIRROR_INTERVAL_MS * 2 - slack, `second round at ${at[1]}ms, before two cadences`);
});

// A mirror tick reads a pane's surface before it reads a frame.
const TARGETS: { name: string; target: { hidden: boolean; navigating: boolean }; want: boolean }[] = [
  { name: 'a shown pane between navigations is the only attempt', target: { hidden: false, navigating: false }, want: true },
  { name: 'a parked pane has no surface to read', target: { hidden: true, navigating: false }, want: false },
  { name: 'a pane whose main frame is navigating has lost one', target: { hidden: false, navigating: true }, want: false },
  { name: 'a pane that is both is still not an attempt', target: { hidden: true, navigating: true }, want: false },
];

for (const c of TARGETS) {
  test(`capturable: ${c.name}`, () => {
    assert.equal(capturable(c.target), c.want);
  });
}
