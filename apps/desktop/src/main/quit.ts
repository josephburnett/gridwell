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
  // Posts the pending trace while the door is still up: the sidecar serves it,
  // so after stopSidecar there is nowhere to send how the app ended.
  flushTrace: () => Promise<void>;
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

  private set(fn: () => void, ms: number): Timer {
    return (this.deps.setTimer ?? ((f: () => void, m: number) => setTimeout(f, m)))(fn, ms);
  }

  private clear(t: Timer): void {
    (this.deps.clearTimer ?? ((x: Timer) => clearTimeout(x as NodeJS.Timeout)))(t);
  }

  // capped resolves when p settles or the bound elapses, whichever comes
  // first. Everything the teardown waits on is best-effort: a view that will
  // not detach, or a post the door never answers, must not leave the app with
  // no window and the sidecar still running.
  private capped(p: Promise<unknown>): Promise<void> {
    return new Promise<void>((resolve) => {
      const t = this.set(resolve, QUIT_FLUSH_WATCHDOG_MS);
      const settle = (): void => {
        this.clear(t);
        resolve();
      };
      void p.then(settle, settle);
    });
  }

  begin(): void {
    if (this.started) return;
    this.started = true;
    trace({ src: 'quit', kind: 'begin', msg: 'closing the windows' });
    this.timer = this.set(() => this.finish(), QUIT_FLUSH_WATCHDOG_MS);
    void this.deps.closeWindows().then(
      () => this.finish(),
      () => this.finish(),
    );
  }

  private finish(): void {
    if (this.flushed) return;
    this.flushed = true;
    this.clear(this.timer);
    this.timer = null;
    this.deps.stopMirror();
    trace({ src: 'quit', kind: 'teardown', msg: 'the views are detaching and the last batch goes now' });
    const done = (): void => {
      this.deps.stopSidecar();
      this.deps.quit();
    };
    void this.capped(Promise.all([this.deps.removeAll(), this.deps.flushTrace()])).then(done, done);
  }
}
