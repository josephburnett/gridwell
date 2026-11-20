import { test } from 'node:test';
import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import { EV } from './ipc';
import { CURSOR_POLL_MS, ForwardedDrag, forwardedDragStep } from './forwardeddrag';
import type { TraceEvent } from './trace';

test('forwardedDragStep: a move tells and stops the clock, a leave starts it, a release or a press ends', () => {
  assert.deepEqual(forwardedDragStep('mouseMove'), { to: EV.rightMoveForward, clock: 'stop' });
  assert.deepEqual(forwardedDragStep('mouseLeave'), { clock: 'start' });
  assert.deepEqual(forwardedDragStep('mouseUp', 'right'), { to: EV.rightUpForward, end: 'view-release' });
  assert.deepEqual(forwardedDragStep('mouseUp', 'left'), {}, 'another button coming up is not the release');
  assert.deepEqual(forwardedDragStep('mouseDown', 'right'), { end: 'view-press' }, 'a new press means the release was lost');
  assert.deepEqual(forwardedDragStep('mouseWheel'), {});
});

// The window's content starts at (100, 50) on screen.
const WIN = { getContentBounds: () => ({ x: 100, y: 50 }) };

function harness() {
  const sent: { channel: string; payload: any }[] = [];
  const traces: TraceEvent[] = [];
  const timers = new Map<number, () => void>();
  let next = 0;
  const cursor = { sx: 0, sy: 0 };
  const drag = new ForwardedDrag({
    win: WIN,
    send: (channel, payload) => sent.push({ channel, payload }),
    cursor: () => ({ ...cursor }),
    trace: (ev) => traces.push(ev),
    setTimer: (fn, ms) => {
      assert.equal(ms, CURSOR_POLL_MS);
      timers.set(++next, fn);
      return next;
    },
    clearTimer: (t) => timers.delete(t as number),
  });
  const view = new EventEmitter();
  const input = (type: string, extra: Record<string, unknown> = {}) => view.emit('input-event', {}, { type, ...extra });
  // tick runs the one armed clock, as its time coming would.
  const tick = () => {
    assert.equal(timers.size, 1, 'one clock armed');
    const [[id, fn]] = [...timers];
    timers.delete(id);
    fn();
  };
  return { drag, view, input, tick, sent, traces, timers, cursor };
}

const moves = (sent: { channel: string; payload: any }[]) => sent.filter((s) => s.channel === EV.rightMoveForward).map((s) => s.payload);

test('start tells the page under an id and follows the view as it tells', () => {
  const h = harness();
  h.drag.start(h.view, 'p1');
  assert.deepEqual(h.sent, [{ channel: EV.rightForward, payload: { id: 1 } }]);
  assert.deepEqual(h.traces, [{ src: 'webview', kind: 'drag-follow', msg: '', kv: { view: 'p1' } }]);
  h.input('mouseMove', { globalX: 130, globalY: 80 });
  assert.deepEqual(moves(h.sent), [{ x: 30, y: 30 }]);
  assert.equal(h.timers.size, 0, 'no clock while the view tells');
});

test('a leave starts the clock, which tells only a point the page was not told', () => {
  const h = harness();
  h.drag.start(h.view, 'p1');
  h.input('mouseMove', { globalX: 130, globalY: 80 });
  h.input('mouseLeave');
  assert.equal(h.traces.at(-1)?.kind, 'drag-blind');
  h.cursor.sx = 130;
  h.cursor.sy = 80;
  h.tick();
  assert.equal(moves(h.sent).length, 1, 'the cursor where the page was last told sends nothing');
  h.cursor.sx = 400;
  h.cursor.sy = 250;
  h.tick();
  assert.deepEqual(moves(h.sent).at(-1), { x: 300, y: 200 }, 'a point elsewhere goes through toContentPoint');
  h.tick();
  assert.equal(moves(h.sent).length, 2, 'a cursor that stays sends nothing more');
  assert.equal(h.timers.size, 1, 'the clock runs on');
});

test('a view move stops the clock, and a second leave starts it again', () => {
  const h = harness();
  h.drag.start(h.view, 'p1');
  h.input('mouseLeave');
  h.input('mouseLeave');
  assert.equal(h.timers.size, 1, 'one clock for two leaves');
  h.input('mouseMove', { globalX: 110, globalY: 60 });
  assert.equal(h.timers.size, 0, 'the view tells again');
  h.input('mouseLeave');
  assert.equal(h.timers.size, 1);
  assert.equal(h.traces.filter((t) => t.kind === 'drag-blind').length, 2);
});

const ends: [string, (h: ReturnType<typeof harness>) => void, Record<string, unknown>?][] = [
  ['view-release', (h) => h.input('mouseUp', { button: 'right', globalX: 140, globalY: 90 })],
  ['view-press', (h) => h.input('mouseDown', { button: 'left' })],
  ['page', (h) => h.drag.pageEnded(1)],
  ['destroyed', (h) => h.view.emit('destroyed')],
  ['superseded', (h) => h.drag.start(new EventEmitter(), 'p2')],
];

for (const [how, endIt] of ends) {
  test(`a follow ended by ${how} stops the clock, lets go of the view and sends nothing after`, () => {
    const h = harness();
    h.drag.start(h.view, 'p1');
    h.input('mouseMove', { globalX: 110, globalY: 60 });
    h.input('mouseLeave');
    h.cursor.sx = 200;
    h.tick();
    endIt(h);
    assert.equal(h.view.listenerCount('input-event'), 0);
    assert.equal(h.view.listenerCount('destroyed'), 0);
    assert.equal(h.timers.size, 0, 'the clock stopped');
    const end = h.traces.find((t) => t.kind === 'drag-end');
    assert.deepEqual(end, { src: 'webview', kind: 'drag-end', msg: how, kv: { view: 'p1', how, told: '1', polled: '1' } });
    const before = h.sent.length;
    h.input('mouseMove', { globalX: 120, globalY: 70 });
    h.input('mouseLeave');
    h.drag.pageEnded(1);
    assert.equal(h.sent.length, before, 'nothing is sent after the end');
    assert.equal(h.traces.filter((t) => t.kind === 'drag-end').length, 1, 'one end');
  });
}

test('the view release is forwarded before the follow ends', () => {
  const h = harness();
  h.drag.start(h.view, 'p1');
  h.input('mouseUp', { button: 'right', globalX: 140, globalY: 90 });
  assert.deepEqual(h.sent.at(-1), { channel: EV.rightUpForward, payload: { x: 40, y: 40 } });
});

test("the page's word for an earlier forward ends nothing", () => {
  const h = harness();
  h.drag.start(h.view, 'p1');
  const v2 = new EventEmitter();
  h.drag.start(v2, 'p2');
  assert.deepEqual(h.sent.at(-1), { channel: EV.rightForward, payload: { id: 2 } });
  h.drag.pageEnded(1);
  assert.equal(v2.listenerCount('input-event'), 1, 'the second follow is still on');
  h.drag.pageEnded(2);
  assert.equal(v2.listenerCount('input-event'), 0);
});
