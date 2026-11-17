// Typing goes to the keyboard's owner, which the renderer derives
// (client/gesture.Keyboard) and tells main (CH.setKeyboard): the root
// webContents, or a live view's. These are main's two verdicts on making it
// true in the OS; webviews.ts owns the state, the timer and the one call that
// focuses a webContents (WebviewRegistry.applyKeyboard).

// How long the guard waits before it decides, and again to confirm. Chromium
// focuses the widget while it routes a press and forwards the press 0.1-3.8 ms
// later, and emits no event for a widget-focus commit; a focus() from inside
// the focus handler is swallowed by that commit. A page's own grab tells
// nothing, so it is the one thing a clock decides.
export const FOCUS_SETTLE_MS = 120;

// touchScroll injects `mouseWheel` through sendInputEvent, which also raises
// `input-event`, so a wheel is not a press.
export function isPressInput(type: string): boolean {
  return type === 'mouseDown' || type === 'touchStart' || type === 'pointerDown';
}

type ApplyAction = 'focus' | 'none' | 'wait-window';

// decideApply: the owner's webContents takes OS focus unless it holds it. Never
// while the window is not focused, since focusing a webContents raises its
// window over the user's desktop; the window's own focus event applies it, once
// the platform has restored its first responder.
export function decideApply(i: { windowFocused: boolean; ownerHoldsOSFocus: boolean }): ApplyAction {
  if (!i.windowFocused) return 'wait-window';
  return i.ownerHoldsOSFocus ? 'none' : 'focus';
}

// The raw grab, where nothing is knowable yet, or the deferred verdict.
export type GuardPhase = 'focus-event' | 'settle';

export interface GuardInput {
  phase: GuardPhase;
  // The webContents that took focus is the one the owner names.
  isOwner: boolean;
  windowFocused: boolean;
  // webContents.isFocused(). At 'focus-event' the event itself is the evidence,
  // so the executor passes true.
  holdsOSFocus: boolean;
  // A press between the two is the user's, and the renderer names its owner
  // next. The count is monotonic, so there is no clock to step backwards.
  pressesAtFocus: number;
  pressesNow: number;
  // alreadyApplied keeps the confirmation from chaining forever.
  alreadyApplied: boolean;
}

export type GuardAction =
  | { kind: 'allow' }
  | { kind: 'wait'; settleMs: number }
  // settleMs is null when this one was already the confirmation.
  | { kind: 'apply'; settleMs: number | null };

// decideFocus: a webContents that took OS focus without being the owner, with
// no press behind it, gives it back to the owner.
export function decideFocus(i: GuardInput): GuardAction {
  if (i.isOwner) return { kind: 'allow' };
  // The window's focus event applies the owner; see decideApply.
  if (!i.windowFocused) return { kind: 'allow' };
  // The owner took it back, or the user moved on; nothing is left to fix.
  if (!i.holdsOSFocus) return { kind: 'allow' };
  if (i.pressesNow > i.pressesAtFocus) return { kind: 'allow' };
  if (i.phase === 'focus-event') return { kind: 'wait', settleMs: FOCUS_SETTLE_MS };
  return { kind: 'apply', settleMs: i.alreadyApplied ? null : FOCUS_SETTLE_MS };
}
