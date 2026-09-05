// focusguard owns the whole "may this live view keep OS keyboard focus?"
// decision. It is pure: no Electron import, no clock read, no timer. webviews.ts
// is the executor — it subscribes to the events, counts the presses, runs the
// timer, and calls onFocusStolen — and makes no comparison of its own.
//
// The rule a live url view obeys: it may hold OS keyboard focus only when its
// pane is the focused pane, or when the user just pressed into it. Anything else
// is a page-initiated steal (a self-refresh, a meta-refresh, a scripted reload
// focuses the new document's widget) and focus goes back to the root window,
// where the canvas and every shell overlay live.
//
// Why the decision is deferred instead of taken in the focus handler, measured
// in docs/debt/w2-native-seam.md §5 on a real OS click, 8/8: Chromium focuses
// the widget in the browser process *while routing the press*, and forwards the
// press afterwards. At the instant the `focus` event fires, nothing about the
// press has happened yet — not the browser-process `input-event` (0.1–3.0 ms
// later), and certainly not the preload's IPC stamp (0.7–3.8 ms later). A guard
// that decides there always reads a stale intent and always bounces the user's
// own first click. So the guard waits one settle and then asks a question that
// has an answer: did a press land in this view *after* this focus?

// FOCUS_SETTLE_MS is how long the guard waits after a focus grab before it
// decides, and again after a bounce before it confirms.
//
// It is the one constant left, and it is unavoidable: Chromium emits no event
// for a widget-focus commit, so there is nothing to wait *on*. M3 measured what
// it is worth — an immediate `rootWC.focus()` from inside the focus handler is
// swallowed by the in-flight commit every time (no view `blur`, no root
// `focus`), while the bounce 121 ms later is the one that actually moves focus
// back. The settle is therefore not a delay bolted onto a working bounce; it is
// the only bounce that works. It is also long enough for the press correlation
// above, which needs 3 ms, and short enough that leaked keystrokes stay
// negligible.
export const FOCUS_SETTLE_MS = 120;

// isPressInput reports whether an Electron InputEvent type is a press into the
// view — the one legitimate way a live view acquires OS focus. Typed narrowly
// on purpose: the registry's own touchScroll injects `mouseWheel` through
// sendInputEvent, which also raises `input-event`, and a wheel is not intent to
// focus. Touch and pen presses count, because a tap on a touch host is the same
// gesture as a click.
export function isPressInput(type: string): boolean {
  return type === 'mouseDown' || type === 'touchStart' || type === 'pointerDown';
}

// GuardPhase is which step of the decision this is. 'focus-event' is the raw
// grab, where nothing is knowable yet; 'settle' is the deferred verdict, and
// also the confirmation after a bounce.
export type GuardPhase = 'focus-event' | 'settle';

export interface GuardInput {
  phase: GuardPhase;
  // paneFocused is Entry.focused: whether the renderer says this pane is the
  // focused pane. The renderer owns it and carries it on place and setHidden.
  paneFocused: boolean;
  // viewHoldsOSFocus is webContents.isFocused(). At 'focus-event' the event
  // itself is the evidence, so the executor passes true.
  viewHoldsOSFocus: boolean;
  // pressesAtFocus is the view's press count snapshotted when the focus event
  // fired; pressesNow is the count now. A press between the two is the user's
  // click arriving, which is what makes this focus legitimate. A monotonic
  // count rather than two timestamps: no clock to step backwards, and no
  // window to tune.
  pressesAtFocus: number;
  pressesNow: number;
  // alreadyBounced is whether this focus has been bounced once already, so the
  // confirmation does not chain forever.
  alreadyBounced: boolean;
}

export type GuardAction =
  // allow: this view may keep OS focus. Nothing to do, nothing to schedule.
  | { kind: 'allow' }
  // wait: nothing is knowable yet. Do not bounce; ask again in settleMs.
  | { kind: 'wait'; settleMs: number }
  // bounce: report the steal. settleMs schedules the confirmation, or null when
  // this was already the confirmation.
  | { kind: 'bounce'; settleMs: number | null };

// decideFocus is the whole guard. Every arm is a fact someone owns: the
// renderer's paneFocused, Chromium's viewHoldsOSFocus, and main's own press
// count. Nothing here reads a clock.
export function decideFocus(i: GuardInput): GuardAction {
  // The renderer says this pane is focused: its view is entitled to OS focus.
  if (i.paneFocused) return { kind: 'allow' };
  // Chromium says the view no longer holds focus — a bounce landed, or the
  // user moved on. There is nothing left to take back.
  if (!i.viewHoldsOSFocus) return { kind: 'allow' };
  // A press landed in this view after the focus: the user's own click, whose
  // widget focus Chromium had already applied before forwarding the press.
  if (i.pressesNow > i.pressesAtFocus) return { kind: 'allow' };
  // Nothing is knowable at the focus event itself; the press, if there is one,
  // is still in flight. Defer.
  if (i.phase === 'focus-event') return { kind: 'wait', settleMs: FOCUS_SETTLE_MS };
  // Unfocused pane, view holds focus, no press explains it: a steal.
  return { kind: 'bounce', settleMs: i.alreadyBounced ? null : FOCUS_SETTLE_MS };
}
