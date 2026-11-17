import { test } from 'node:test';
import assert from 'node:assert/strict';
import { decideApply, decideFocus, isPressInput, FOCUS_SETTLE_MS, GuardInput, GuardAction } from './focusguard';

// Every cell of applying the owner: the window focused or not, times the
// owner's webContents holding OS focus or not.
test('decideApply: the owner takes OS focus only in a focused window that it does not hold', () => {
  assert.equal(decideApply({ windowFocused: true, ownerHoldsOSFocus: false }), 'focus');
  assert.equal(decideApply({ windowFocused: true, ownerHoldsOSFocus: true }), 'none');
  assert.equal(decideApply({ windowFocused: false, ownerHoldsOSFocus: false }), 'wait-window');
  assert.equal(decideApply({ windowFocused: false, ownerHoldsOSFocus: true }), 'wait-window');
});

// A webContents that is not the owner took OS focus in a focused window with
// no press behind it, the steal shape. Each case overrides only what it is
// about.
function input(over: Partial<GuardInput> = {}): GuardInput {
  return {
    phase: 'focus-event',
    isOwner: false,
    windowFocused: true,
    holdsOSFocus: true,
    pressesAtFocus: 0,
    pressesNow: 0,
    alreadyApplied: false,
    ...over,
  };
}

const TABLE: { name: string; in: GuardInput; want: GuardAction }[] = [
  { name: 'the owner taking focus is allowed', in: input({ isOwner: true }), want: { kind: 'allow' } },
  { name: 'the owner, at the settle too', in: input({ isOwner: true, phase: 'settle' }), want: { kind: 'allow' } },
  {
    // At the focus event the press that may explain it has not arrived.
    name: 'a grab by another decides nothing yet',
    in: input(),
    want: { kind: 'wait', settleMs: FOCUS_SETTLE_MS },
  },
  { name: 'still held at the settle with no press: the owner takes it back', in: input({ phase: 'settle' }), want: { kind: 'apply', settleMs: FOCUS_SETTLE_MS } },
  { name: 'the confirmation applies once and stops chaining', in: input({ phase: 'settle', alreadyApplied: true }), want: { kind: 'apply', settleMs: null } },
  { name: 'a press since the grab is the user\'s', in: input({ phase: 'settle', pressesNow: 1 }), want: { kind: 'allow' } },
  { name: 'a press already in at the grab', in: input({ pressesNow: 1 }), want: { kind: 'allow' } },
  {
    // A guard keyed on how recently a click happened would let a page steal
    // focus in the shadow of an earlier click.
    name: 'an older press, consumed by an earlier focus, is not this one',
    in: input({ phase: 'settle', pressesAtFocus: 7, pressesNow: 7 }),
    want: { kind: 'apply', settleMs: FOCUS_SETTLE_MS },
  },
  { name: 'a press during the confirmation stops it', in: input({ phase: 'settle', alreadyApplied: true, pressesAtFocus: 2, pressesNow: 3 }), want: { kind: 'allow' } },
  { name: 'no longer held at the settle', in: input({ phase: 'settle', holdsOSFocus: false }), want: { kind: 'allow' } },
  { name: 'a window not focused is the platform restoring its first responder', in: input({ windowFocused: false }), want: { kind: 'allow' } },
  { name: 'not focused at the settle either', in: input({ phase: 'settle', windowFocused: false }), want: { kind: 'allow' } },
];

for (const c of TABLE) {
  test(`decideFocus: ${c.name}`, () => {
    assert.deepEqual(decideFocus(c.in), c.want);
  });
}

test('isPressInput: a press is a mouse, touch or pointer down; a wheel is not', () => {
  for (const t of ['mouseDown', 'touchStart', 'pointerDown']) assert.equal(isPressInput(t), true, t);
  for (const t of ['mouseUp', 'mouseMove', 'mouseWheel', 'keyDown']) assert.equal(isPressInput(t), false, t);
});
