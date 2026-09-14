import type { NativeImage, WebContentsView } from 'electron';

// A frozen preview is zoomed up in larger panes, so it has to stay crisp.
const JPEG_QUALITY = 92;

// A parked or busy renderer can leave capturePage pending forever, and the
// freeze path detaches only after it resolves, so an unbounded wait strands the
// view over the pane just left.
export const CAPTURE_TIMEOUT_MS = 1500;

// One attempt's outcome, because a caller that sees only '' cannot tell a
// wedged renderer from a blank page. The kind is capturestreak's AttemptKind.
export type CaptureAttempt =
  | { kind: 'ok'; jpegBase64: string }
  | { kind: 'empty' }
  | { kind: 'timeout'; timeoutMs: number }
  | { kind: 'rejected'; error: unknown }
  | { kind: 'view-gone'; error: unknown };

// capturePage works on the visible attached view, so no offscreen mode is
// needed. Every way it can fail is a case here, because from outside they all
// look like a preview that froze.
export async function captureAttempt(
  view: WebContentsView,
  timeoutMs = CAPTURE_TIMEOUT_MS,
): Promise<CaptureAttempt> {
  let pending: Promise<NativeImage>;
  try {
    // Reading webContents off a destroyed view throws synchronously.
    pending = view.webContents.capturePage();
  } catch (err) {
    return { kind: 'view-gone', error: err };
  }
  const settled = await settleWithin(pending, timeoutMs);
  if (settled.kind === 'timeout') return { kind: 'timeout', timeoutMs };
  if (settled.kind === 'rejected') return { kind: 'rejected', error: settled.error };
  const image = settled.value;
  if (!image || image.isEmpty()) return { kind: 'empty' };
  const jpegBase64 = image.toJPEG(JPEG_QUALITY).toString('base64');
  // A non-empty image that encodes to nothing is still no frame to show.
  return jpegBase64 ? { kind: 'ok', jpegBase64 } : { kind: 'empty' };
}

// For callers that only want the bytes: '' means no frame, whatever went wrong.
// The view-gone arm is rethrown so remove() reports "view crashed while
// closing".
export async function captureJpegBase64(view: WebContentsView, timeoutMs = CAPTURE_TIMEOUT_MS): Promise<string> {
  const attempt = await captureAttempt(view, timeoutMs);
  if (attempt.kind === 'view-gone') throw attempt.error;
  return attempt.kind === 'ok' ? attempt.jpegBase64 : '';
}

// The report the user reads. 'ok' is here so a new kind cannot be added with no
// description.
export function describeAttempt(attempt: CaptureAttempt): string {
  switch (attempt.kind) {
    case 'ok':
      return 'ok';
    case 'empty':
      return 'the renderer produced an empty frame';
    case 'timeout':
      return `capturePage did not answer within ${attempt.timeoutMs}ms`;
    case 'rejected':
      return `capturePage failed: ${String(attempt.error)}`;
    case 'view-gone':
      return `the view is gone: ${String(attempt.error)}`;
  }
}

// Settled keeps a timeout and a rejection distinguishable downstream.
type Settled<T> =
  | { kind: 'value'; value: T }
  | { kind: 'timeout' }
  | { kind: 'rejected'; error: unknown };

// settleWithin resolves, and never rejects, with what p did, or 'timeout'.
function settleWithin<T>(p: Promise<T>, ms: number): Promise<Settled<T>> {
  return new Promise<Settled<T>>((resolve) => {
    let done = false;
    const finish = (s: Settled<T>) => {
      if (done) return;
      done = true;
      clearTimeout(timer);
      resolve(s);
    };
    const timer = setTimeout(() => finish({ kind: 'timeout' }), ms);
    p.then(
      (value) => finish({ kind: 'value', value }),
      (error) => finish({ kind: 'rejected', error }),
    );
  });
}

// How often live views are captured so other panes showing the same tile
// mirror them. Previews need no frame rate, so the cadence is low.
export const MIRROR_INTERVAL_MS = 250;

type Timer = unknown;

interface PumpTimers {
  // Seams, so a test can read the scheduled cadence instead of waiting it out.
  setTimer?: (fn: () => void, ms: number) => Timer;
  clearTimer?: (timer: Timer) => void;
}

// MirrorPump captures every live pane on a timer, so a tile mirrored in a
// second pane stays fresh. It owns the cadence: a caller that picked its own
// would be a second copy of a decision about how fresh a preview has to be.
export class MirrorPump {
  private timer: Timer = null;
  private readonly setTimer: (fn: () => void, ms: number) => Timer;
  private readonly clearTimer: (timer: Timer) => void;

  constructor(
    private readonly tick: () => Promise<void>,
    timers: PumpTimers = {},
  ) {
    this.setTimer = timers.setTimer ?? ((fn, ms) => setTimeout(fn, ms));
    this.clearTimer = timers.clearTimer ?? ((t) => clearTimeout(t as NodeJS.Timeout));
  }

  start(): void {
    if (this.timer) return;
    const loop = async () => {
      // One failed round must not end the pump: every mirrored preview would
      // stay frozen until the app restarts.
      await this.tick().catch(() => {});
      if (this.timer) this.timer = this.setTimer(loop, MIRROR_INTERVAL_MS);
    };
    this.timer = this.setTimer(loop, MIRROR_INTERVAL_MS);
  }

  stop(): void {
    if (this.timer) {
      this.clearTimer(this.timer);
      this.timer = null;
    }
  }
}
