import { test } from 'node:test';
import assert from 'node:assert/strict';
import { QuitFlush, QUIT_FLUSH_WATCHDOG_MS } from './quit';

// A recorder of the teardown steps, with the windows and the timer held open so
// each test decides which ends the wait.
function harness(opts: { removeAllFails?: boolean } = {}) {
  const steps: string[] = [];
  let closeWindows!: () => void;
  const windows = new Promise<void>((res) => (closeWindows = res));
  let fire: (() => void) | null = null;
  let scheduledMs: number | null = null;
  let cleared = false;
  const flush = new QuitFlush({
    closeWindows: () => windows,
    stopMirror: () => steps.push('stopMirror'),
    removeAll: () => {
      steps.push('removeAll');
      return opts.removeAllFails ? Promise.reject(new Error('view will not detach')) : Promise.resolve();
    },
    stopSidecar: () => steps.push('stopSidecar'),
    flushTrace: () => steps.push('flushTrace'),
    stopTrace: () => steps.push('stopTrace'),
    quit: () => steps.push('quit'),
    setTimer: (fn, ms) => {
      scheduledMs = ms;
      fire = fn;
      return 'watchdog';
    },
    clearTimer: (t) => {
      assert.equal(t, 'watchdog');
      cleared = true;
    },
  });
  // The teardown's tail runs off removeAll's promise.
  const settle = () => new Promise<void>((r) => setImmediate(r));
  return {
    flush,
    steps,
    closeWindows,
    settle,
    watchdog: () => fire!(),
    scheduledMs: () => scheduledMs,
    cleared: () => cleared,
  };
}

test('the windows closing ends the wait, and the teardown runs in order', async () => {
  const h = harness();
  h.flush.begin();
  assert.deepEqual(h.steps, ['flushTrace'], 'nothing but the trace post, which needs the windows up');
  h.closeWindows();
  await h.settle();
  assert.deepEqual(h.steps, ['flushTrace', 'stopTrace', 'stopMirror', 'removeAll', 'stopSidecar', 'quit']);
  assert.ok(h.cleared(), 'the watchdog must be cleared, or it fires into a quit already done');
  assert.equal(h.flush.flushed, true);
});

test('a window that never closes is capped by the watchdog', async () => {
  const h = harness();
  h.flush.begin();
  assert.equal(h.scheduledMs(), QUIT_FLUSH_WATCHDOG_MS, 'the wait is capped at the declared bound');
  h.watchdog();
  await h.settle();
  assert.deepEqual(h.steps, ['flushTrace', 'stopTrace', 'stopMirror', 'removeAll', 'stopSidecar', 'quit']);
});

test('the watchdog is real time, not an instant give-up nor a promise never kept', async () => {
  const steps: string[] = [];
  const started = Date.now();
  const quit = new Promise<void>((res) => {
    const flush = new QuitFlush({
      // The stall: a renderer whose beforeunload never acknowledges.
      closeWindows: () => new Promise<void>(() => {}),
      stopMirror: () => steps.push('stopMirror'),
      removeAll: () => Promise.resolve(),
      stopSidecar: () => steps.push('stopSidecar'),
      flushTrace: () => steps.push('flushTrace'),
      stopTrace: () => steps.push('stopTrace'),
      quit: () => {
        steps.push('quit');
        res();
      },
    });
    flush.begin();
  });
  const outcome = await Promise.race([
    quit.then(() => 'quit'),
    new Promise<string>((r) => setTimeout(() => r('hung'), QUIT_FLUSH_WATCHDOG_MS * 4)),
  ]);
  assert.equal(outcome, 'quit', `the app never quit, ${QUIT_FLUSH_WATCHDOG_MS * 4}ms after a stalled unload`);
  const waited = Date.now() - started;
  // Timers may fire a hair early; the point is that the flush got its wait and
  // was not abandoned at once.
  assert.ok(waited >= QUIT_FLUSH_WATCHDOG_MS - 50, `quit after ${waited}ms, before the ${QUIT_FLUSH_WATCHDOG_MS}ms bound`);
  assert.deepEqual(steps, ['flushTrace', 'stopTrace', 'stopMirror', 'stopSidecar', 'quit']);
});

test('the windows closing after the watchdog fired tears nothing down twice', async () => {
  const h = harness();
  h.flush.begin();
  h.watchdog();
  await h.settle();
  h.closeWindows();
  await h.settle();
  assert.deepEqual(h.steps, ['flushTrace', 'stopTrace', 'stopMirror', 'removeAll', 'stopSidecar', 'quit']);
});

test('a second before-quit neither restarts the sequence nor blocks the quit', async () => {
  const h = harness();
  h.flush.begin();
  h.flush.begin();
  h.closeWindows();
  await h.settle();
  assert.deepEqual(h.steps, ['flushTrace', 'stopTrace', 'stopMirror', 'removeAll', 'stopSidecar', 'quit']);
  assert.equal(h.flush.flushed, true, 'flushed is what tells before-quit to stop preventing the quit');
});

test('a view that will not detach still stops the sidecar and quits', async () => {
  const h = harness({ removeAllFails: true });
  h.flush.begin();
  h.closeWindows();
  await h.settle();
  assert.deepEqual(h.steps, ['flushTrace', 'stopTrace', 'stopMirror', 'removeAll', 'stopSidecar', 'quit']);
});

// A trace post Chromium is still carrying when the windows go keeps the app
// from exiting at all — the e2e teardown saw a window-less app with a live
// sidecar. So the last batch goes while they are up, and the traffic ends
// before anything else is torn down.
test('the trace posts while the windows are up and stops before the teardown', async () => {
  const h = harness();
  h.flush.begin();
  assert.deepEqual(h.steps, ['flushTrace'], 'the post needs the windows Chromium is about to close');
  h.closeWindows();
  await h.settle();
  assert.equal(h.steps[1], 'stopTrace', 'the traffic ends before the first teardown step');
  assert.ok(
    h.steps.indexOf('stopTrace') < h.steps.indexOf('stopSidecar'),
    'nothing may be in flight when the door and the app go',
  );
});
