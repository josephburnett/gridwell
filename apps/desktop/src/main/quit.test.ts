import { test } from 'node:test';
import assert from 'node:assert/strict';
import { QuitFlush, QUIT_FLUSH_WATCHDOG_MS } from './quit';

// A recorder of the teardown steps, with the windows and the timer held open so
// each test decides which ends the wait.
function harness(opts: { removeAllFails?: boolean; flushFails?: boolean; flushHangs?: boolean } = {}) {
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
    flushTrace: () => {
      steps.push('flushTrace');
      if (opts.flushHangs) return new Promise<void>(() => {});
      return opts.flushFails ? Promise.reject(new Error('the door is gone')) : Promise.resolve();
    },
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
  assert.deepEqual(h.steps, [], 'nothing tears down while the renderers are still flushing');
  h.closeWindows();
  await h.settle();
  assert.deepEqual(h.steps, ['stopMirror', 'removeAll', 'flushTrace', 'stopSidecar', 'quit']);
  assert.ok(h.cleared(), 'the watchdog must be cleared, or it fires into a quit already done');
  assert.equal(h.flush.flushed, true);
});

test('a window that never closes is capped by the watchdog', async () => {
  const h = harness();
  h.flush.begin();
  assert.equal(h.scheduledMs(), QUIT_FLUSH_WATCHDOG_MS, 'the wait is capped at the declared bound');
  h.watchdog();
  await h.settle();
  assert.deepEqual(h.steps, ['stopMirror', 'removeAll', 'flushTrace', 'stopSidecar', 'quit']);
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
      flushTrace: () => {
        steps.push('flushTrace');
        return Promise.resolve();
      },
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
  assert.deepEqual(steps, ['stopMirror', 'flushTrace', 'stopSidecar', 'quit']);
});

test('the windows closing after the watchdog fired tears nothing down twice', async () => {
  const h = harness();
  h.flush.begin();
  h.watchdog();
  await h.settle();
  h.closeWindows();
  await h.settle();
  assert.deepEqual(h.steps, ['stopMirror', 'removeAll', 'flushTrace', 'stopSidecar', 'quit']);
});

test('a second before-quit neither restarts the sequence nor blocks the quit', async () => {
  const h = harness();
  h.flush.begin();
  h.flush.begin();
  h.closeWindows();
  await h.settle();
  assert.deepEqual(h.steps, ['stopMirror', 'removeAll', 'flushTrace', 'stopSidecar', 'quit']);
  assert.equal(h.flush.flushed, true, 'flushed is what tells before-quit to stop preventing the quit');
});

test('a view that will not detach still stops the sidecar and quits', async () => {
  const h = harness({ removeAllFails: true });
  h.flush.begin();
  h.closeWindows();
  await h.settle();
  assert.deepEqual(h.steps, ['stopMirror', 'removeAll', 'flushTrace', 'stopSidecar', 'quit']);
});

// The door goes down with the sidecar, so the last post is made while it is
// still up — and a refused one must not strand the app with no window.
test('a trace post that will not land still stops the sidecar and quits', async () => {
  const h = harness({ flushFails: true });
  h.flush.begin();
  h.closeWindows();
  await h.settle();
  assert.deepEqual(h.steps, ['stopMirror', 'removeAll', 'flushTrace', 'stopSidecar', 'quit']);
});

// The trace door is served by the sidecar, over a network stack that is coming
// down with the app: a post that never answers must not leave the user with no
// window and a backend still running. The same bound caps it as caps the
// renderers' unload.
test('a trace post that never answers still stops the sidecar and quits', async () => {
  const h = harness({ flushHangs: true });
  h.flush.begin();
  h.closeWindows();
  await h.settle();
  assert.deepEqual(h.steps, ['stopMirror', 'removeAll', 'flushTrace'], 'the teardown is waiting on the post');
  h.watchdog();
  await h.settle();
  assert.deepEqual(h.steps, ['stopMirror', 'removeAll', 'flushTrace', 'stopSidecar', 'quit']);
});
