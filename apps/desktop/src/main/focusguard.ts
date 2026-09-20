// A live url view may keep OS keyboard focus only when its pane is focused or
// the user just pressed into it; anything else is a page-initiated steal and
// focus returns to the root window. The verdict waits one settle because
// Chromium focuses the widget while it routes the press and forwards the press
// 0.1-3.8 ms later. webviews.ts owns the state and timer.

// How long the guard waits before it decides, and again to confirm a bounce.
// Chromium emits no event for a widget-focus commit, and a rootWC.focus() from
// inside the focus handler is swallowed by that commit.
export const FOCUS_SETTLE_MS = 120;

// touchScroll injects `mouseWheel` through sendInputEvent, which also raises
// `input-event`, so a wheel is not a press.
export function isPressInput(type: string): boolean {
  return type === 'mouseDown' || type === 'touchStart' || type === 'pointerDown';
}

// The raw grab, where nothing is knowable yet, or the deferred verdict.
export type GuardPhase = 'focus-event' | 'settle';

export interface GuardInput {
  phase: GuardPhase;
  // Entry.focused, which the renderer owns.
  paneFocused: boolean;
  // webContents.isFocused(). At 'focus-event' the event itself is the evidence,
  // so the executor passes true.
  viewHoldsOSFocus: boolean;
  // A press between the two is the user's click arriving. The count is
  // monotonic, so there is no clock to step backwards.
  pressesAtFocus: number;
  pressesNow: number;
  // alreadyBounced keeps the confirmation from chaining forever.
  alreadyBounced: boolean;
}

export type GuardAction =
  | { kind: 'allow' }
  | { kind: 'wait'; settleMs: number }
  // settleMs is null when this bounce was already the confirmation.
  | { kind: 'bounce'; settleMs: number | null };

// Every arm reads a fact someone else owns; this package keeps none.
export function decideFocus(i: GuardInput): GuardAction {
  if (i.paneFocused) return { kind: 'allow' };
  // A bounce landed, or the user moved on; nothing is left to take back.
  if (!i.viewHoldsOSFocus) return { kind: 'allow' };
  if (i.pressesNow > i.pressesAtFocus) return { kind: 'allow' };
  if (i.phase === 'focus-event') return { kind: 'wait', settleMs: FOCUS_SETTLE_MS };
  return { kind: 'bounce', settleMs: i.alreadyBounced ? null : FOCUS_SETTLE_MS };
}
