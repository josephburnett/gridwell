import { EV, type ForwardedRightdown, type ScreenPoint } from './ipc';
import type { TraceEvent } from './trace';
import { toContentPoint } from './viewutil';
import { dragBlind, dragEnded, dragFollowed } from './viewtrace';

// A right-drag that became a pane gesture over a live url view, which the view
// keeps, re-aimed at the root page. On macOS the view hears it only while the
// pointer is over it (Chromium's RenderWidgetHostViewCocoa drops a right-held
// move off its bounds and says mouseLeave once), so from a leave main reads
// the cursor itself.

// One frame: how often main reads the cursor while the view cannot tell it.
export const CURSOR_POLL_MS = 16;

// How a follow ended; superseded is another view's drag beginning.
type FollowEnd = 'view-release' | 'view-press' | 'page' | 'destroyed' | 'superseded';

// What one of the view's input events is to the follow: the channel to re-aim
// it on, whether the view can tell the pointer (clock stop) or no longer can
// (clock start), and whether the drag is over.
export function forwardedDragStep(type: string, button?: string): { to?: string; clock?: 'start' | 'stop'; end?: FollowEnd } {
  if (type === 'mouseMove') return { to: EV.rightMoveForward, clock: 'stop' };
  if (type === 'mouseLeave') return { clock: 'start' };
  if (type === 'mouseDown') return { end: 'view-press' };
  return type === 'mouseUp' && button === 'right' ? { to: EV.rightUpForward, end: 'view-release' } : {};
}

type Listener = (...args: any[]) => void;
type Timer = unknown;

// The view's webContents, as the follow uses it.
interface DragView {
  on(event: string, fn: Listener): unknown;
  removeListener(event: string, fn: Listener): unknown;
}

interface ForwardedDragDeps {
  win: { getContentBounds(): { x: number; y: number } };
  send: (channel: string, payload: unknown) => void;
  cursor: () => ScreenPoint;
  trace: (ev: TraceEvent) => void;
  setTimer?: (fn: () => void, ms: number) => Timer;
  clearTimer?: (timer: Timer) => void;
}

// ForwardedDrag owns the one follow there is at a time.
export class ForwardedDrag {
  private seq = 0;
  private end: (how: FollowEnd) => void = () => {};
  private readonly setTimer: (fn: () => void, ms: number) => Timer;
  private readonly clearTimer: (timer: Timer) => void;

  constructor(private readonly d: ForwardedDragDeps) {
    this.setTimer = d.setTimer ?? ((fn, ms) => setTimeout(fn, ms));
    this.clearTimer = d.clearTimer ?? ((t) => clearTimeout(t as NodeJS.Timeout));
  }

  // start tells the page the view's right press became a drag, under an id
  // the page echoes to pageEnded when the gesture it armed is over.
  start(view: DragView, paneId: string): void {
    this.end('superseded');
    const id = ++this.seq;
    let timer: Timer = null;
    let last: ForwardedRightdown | undefined;
    let told = 0;
    let polled = 0;
    const tell = (channel: string, p: ForwardedRightdown): void => {
      last = p;
      this.d.send(channel, p);
    };
    const stop = (): void => {
      if (timer) this.clearTimer(timer);
      timer = null;
    };
    const poll = (): void => {
      timer = this.setTimer(() => {
        const p = toContentPoint(this.d.win, this.d.cursor());
        if (p.x !== last?.x || p.y !== last?.y) {
          polled++;
          tell(EV.rightMoveForward, p);
        }
        poll();
      }, CURSOR_POLL_MS);
    };
    const onInput = (_e: unknown, ie: { type: string; button?: string; globalX?: number; globalY?: number }): void => {
      const step = forwardedDragStep(ie.type, ie.button);
      if (step.clock === 'stop') stop();
      if (step.to === EV.rightMoveForward) told++;
      if (step.to) tell(step.to, toContentPoint(this.d.win, { sx: ie.globalX ?? 0, sy: ie.globalY ?? 0 }));
      if (step.clock === 'start' && !timer) {
        this.d.trace(dragBlind(paneId));
        poll();
      }
      if (step.end) this.end(step.end);
    };
    const onGone = (): void => this.end('destroyed');
    this.end = (how) => {
      this.end = () => {};
      stop();
      view.removeListener('input-event', onInput);
      view.removeListener('destroyed', onGone);
      this.d.trace(dragEnded(paneId, how, told, polled));
    };
    this.pageEnded = (ended) => {
      if (ended === id) this.end('page');
    };
    view.on('input-event', onInput);
    view.on('destroyed', onGone);
    this.d.trace(dragFollowed(paneId));
    this.d.send(EV.rightForward, { id });
  }

  // pageEnded is the page's word that forward id's gesture is over, the one
  // end on macOS, where the release never reaches the view.
  pageEnded: (id: number) => void = () => {};
}
