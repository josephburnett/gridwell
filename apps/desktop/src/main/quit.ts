import { trace } from './trace';

// The quit sequence. Quit is two-phase, because the renderer's unload flush
// needs the views and the sidecar still alive: close the windows, wait for
// each beforeunload, then stop the sidecar. Tearing either down first loses the
// live tile's page, trail and unsaved text.

// The cap on that wait. A renderer that never acknowledges its unload would
// otherwise strand the sidecar and the app with no window.
export const QUIT_FLUSH_WATCHDOG_MS = 2000;

type Timer = unknown;

interface QuitFlushDeps {
  // Resolves once every window has emitted 'closed'.
  closeWindows: () => Promise<void>;
  stopMirror: () => void;
  // Detaches the live views for their localStorage flush.
  removeAll: () => Promise<void>;
  stopSidecar: () => void;
  quit: () => void;
  // Posts what the ring holds, and is never awaited; see stopTrace.
  flushTrace: () => void;
  // Ends the trace's traffic. A request Chromium is still carrying when the
  // windows go keeps the app from exiting at all, so nothing may be in flight
  // past this point.
  stopTrace: () => void;
  // Seams, so a test can watch the watchdog instead of waiting it out.
  setTimer?: (fn: () => void, ms: number) => Timer;
  clearTimer?: (timer: Timer) => void;
}

// One quit. Whichever comes first, the windows or the watchdog, ends the wait
// and the teardown runs exactly once.
export class QuitFlush {
  // True once the wait is over, so a second before-quit lets the quit through.
  flushed = false;
  private started = false;
  private timer: Timer = null;

  constructor(private readonly deps: QuitFlushDeps) {}

  begin(): void {
    if (this.started) return;
    this.started = true;
    trace({ src: 'quit', kind: 'begin', msg: 'closing the windows' });
    // While the windows are still up: the door dies with the sidecar, so this
    // is the last batch that can land.
    this.deps.flushTrace();
    const set = this.deps.setTimer ?? ((fn: () => void, ms: number) => setTimeout(fn, ms));
    this.timer = set(() => this.finish(), QUIT_FLUSH_WATCHDOG_MS);
    void this.deps.closeWindows().then(
      () => this.finish(),
      () => this.finish(),
    );
  }

  private finish(): void {
    if (this.flushed) return;
    this.flushed = true;
    const clear = this.deps.clearTimer ?? ((t: Timer) => clearTimeout(t as NodeJS.Timeout));
    clear(this.timer);
    this.timer = null;
    // The windows are gone, so no post may be in flight from here on.
    this.deps.stopTrace();
    this.deps.stopMirror();
    const done = (): void => {
      this.deps.stopSidecar();
      this.deps.quit();
    };
    // A view that will not detach must not hold the sidecar open.
    void this.deps.removeAll().then(done, done);
  }
}
