import { test, expect } from './fixtures';
import { QUIT_FLUSH_WATCHDOG_MS } from '../src/main/quit';

// Quit waits for every window's unload flush before it stops the sidecar, and
// the watchdog caps that wait. Nothing else in the suite stalls a beforeunload,
// so a watchdog that never fired would look the same from outside: this spec
// wedges the renderer's unload for four times the bound and asserts the sidecar
// still goes down at the bound.

// A beforeunload that does not return for this long: long enough that an
// uncapped wait is unmistakable, short enough that the window still closes and
// teardown stays clean.
const STALL_MS = QUIT_FLUSH_WATCHDOG_MS * 4;

function alive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch (err: unknown) {
    return (err as NodeJS.ErrnoException).code !== 'ESRCH';
  }
}

test('a renderer that stalls its unload does not hold the quit past the watchdog', async ({ window, electronApp }) => {
  const pid = await electronApp.evaluate(
    () => (globalThis as { __gwSidecarPid?: number }).__gwSidecarPid ?? null,
  );
  expect(pid).not.toBeNull();

  // A synchronous spin, because that is what a renderer too busy to
  // acknowledge its unload looks like to the main process.
  // globalThis, because the spec's `window` fixture shadows the page's.
  await window.evaluate((ms) => {
    globalThis.addEventListener('beforeunload', () => {
      const until = Date.now() + ms;
      while (Date.now() < until) {
        /* spin */
      }
    });
  }, STALL_MS);

  const started = Date.now();
  // The app exits underneath this call, so the evaluate promise need not settle.
  void electronApp.evaluate(({ app }) => app.quit()).catch(() => {});

  while (alive(pid!) && Date.now() - started < STALL_MS) {
    await new Promise((r) => setTimeout(r, 50));
  }
  const waited = Date.now() - started;
  expect(alive(pid!), `sidecar still running ${waited}ms after quit, with a ${STALL_MS}ms unload stall`).toBe(false);
  // Slack for the SIGTERM and the sidecar's own exit; the uncapped wait would
  // be STALL_MS, far past this.
  expect(waited, 'the sidecar outlived the watchdog, so the quit waited on the stalled renderer').toBeLessThan(
    QUIT_FLUSH_WATCHDOG_MS + 2_000,
  );
});
