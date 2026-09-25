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

// A pump whose timer is a seam, so a test can read the delay it scheduled.
function pumpHarness(tick: () => Promise<void>) {
  const scheduled: number[] = [];
  let fire: (() => void) | null = null;
  let cleared = 0;
  const pump = new MirrorPump(tick, {
    setTimer: (fn, ms) => {
      scheduled.push(ms);
      fire = fn;
      return 'pump';
    },
    clearTimer: (t) => {
      assert.equal(t, 'pump');
      cleared += 1;
    },
  });
  // A tick's tail runs off its own promise.
  const settle = () => new Promise<void>((r) => setImmediate(r));
  return { pump, scheduled, cleared: () => cleared, fire: () => fire!(), settle };
}

test('every round is scheduled at the declared cadence', async () => {
  const h = pumpHarness(async () => {});
  h.pump.start();
  assert.deepEqual(h.scheduled, [MIRROR_INTERVAL_MS], 'the first round waits the cadence');
  h.fire();
  await h.settle();
  h.fire();
  await h.settle();
  assert.deepEqual(
    h.scheduled,
    [MIRROR_INTERVAL_MS, MIRROR_INTERVAL_MS, MIRROR_INTERVAL_MS],
    'a round that finished must not pull the next one in',
  );
  h.pump.stop();
  assert.equal(h.cleared(), 1);
});

test('a round that throws does not end the pump', async () => {
  let ticks = 0;
  const h = pumpHarness(async () => {
    ticks += 1;
    throw new Error('capturePage blew up');
  });
  h.pump.start();
  h.fire();
  await h.settle();
  assert.equal(ticks, 1);
  assert.equal(h.scheduled.length, 2, 'a failed round still schedules the next');
  h.fire();
  await h.settle();
  assert.equal(ticks, 2, 'the pump keeps capturing after a failure');
});

test('the default timer waits the declared cadence', async () => {
  const at: number[] = [];
  const started = Date.now();
  const pump = new MirrorPump(async () => {
    at.push(Date.now() - started);
  });
  pump.start();
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
