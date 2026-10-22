import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { TraceLine } from './trace';
import { attribute, budgetVerdict, CAUSE_MS, GestureWindow, unattributed } from './tracebudget';

let seq = 0;
function rec(origin: string, src: string, kind: string, msg: string, t: number, kv?: Record<string, string>): TraceLine {
  return { seq: ++seq, t, origin, src, kind, msg, kv };
}
const rpc = (msg: string, t: number, kv: Record<string, string> = {}) => rec('node', 'router', 'rpc', msg, t, { req: '', ...kv });
const windows: GestureWindow[] = [{ name: 'g', start: 1_000, end: 2_000 }];
const causes = (lines: TraceLine[]) => attribute(lines, windows).map((v) => v.cause);

test("a call the client made inside a gesture is the gesture's", () => {
  assert.deepEqual(causes([rpc('GetGrid start', 1_500, { req: 'r1' }), rpc('GetGrid ok', 1_510, { req: 'r1' })]), ['gesture', 'gesture']);
});

test('a call the client made in the quiet, with nothing telling it, is unattributed', () => {
  const lines = [rpc('GetGrid start', 5_000, { req: 'r2', id: 'g/1' }), rpc('GetGrid ok', 5_010, { req: 'r2', id: 'g/1' })];
  assert.deepEqual(causes(lines), [null, null]);
  assert.deepEqual(
    unattributed(attribute(lines, windows)).map((u) => [u.msg, u.id]),
    [
      ['GetGrid start', 'g/1'],
      ['GetGrid ok', 'g/1'],
    ],
  );
});

test("a call that follows an event the client received is the event's, and a resync is the decision's", () => {
  const told = [
    rec('client', 'events', 'recv', 'grid changed', 5_000),
    rec('client', 'rpc', 'rpc', 'GetGrid start', 5_020, { req: 'r3' }),
    rpc('GetGrid start', 5_030, { req: 'r3' }),
  ];
  assert.equal(attribute(told, windows)[0].cause, 'event');
  const resync = [rec('client', 'events', 'resync', '2 grids re-read', 5_000), rpc('GetGrid start', 5_030, { req: 'r4' })];
  assert.equal(attribute(resync, windows)[0].cause, 'decision: stream-gap resync');
  const late = [rec('client', 'events', 'recv', 'grid changed', 5_000), rpc('GetGrid start', 5_001 + CAUSE_MS, { req: 'r5' })];
  assert.equal(attribute(late, windows)[0].cause, null, 'an event long past causes nothing');
});

test('a view is always owed: SetView and Subscribe, in a gesture or not', () => {
  assert.deepEqual(causes([rpc('SetView start', 9_000, { req: 'r6' }), rpc('Subscribe start', 9_000)]), ['view', 'view']);
});

test('a call with no request id is owed to a gesture only when the client wrote in it', () => {
  assert.deepEqual(causes([rpc('GetGrid start', 1_500)]), [null]);
  assert.deepEqual(causes([rec('client', 'gesture', 'press', 'left', 1_400), rpc('GetGrid start', 1_500)]), ['gesture']);
});

test("the source layers: a walk is the decision's, a told change the event's, a Watch the view's", () => {
  const lines = [
    rec('node', 'sourcecache', 'prefetch', 'prefetch start', 9_000),
    rec('node', 'pluginhost', 'changed', 'grid changed', 9_000),
    rpc('SetView start', 9_000, { req: 'r7' }),
    rpc('SetView ok', 9_010, { req: 'r7' }),
    rec('node', 'pluginhost', 'watch', 'watch open', 9_100),
    rec('node', 'pluginhost', 'watch', 'watch open', 9_200 + CAUSE_MS),
  ];
  assert.deepEqual(causes(lines), ['decision: prefetch walk (2026-10-06)', 'event', 'view', 'view', 'view', null]);
});

test("work inside a call is the call's, and a revalidation is the serve's it runs behind", () => {
  const inside = [
    rpc('GetGrid start', 1_500, { req: 'r8', id: 'g/1' }),
    rec('node', 'sourcecache', 'serve', 'serve cached', 1_501, { how: 'cached', grid: 'g/1' }),
    rpc('GetGrid ok', 1_502, { req: 'r8', id: 'g/1' }),
    rec('node', 'sourcecache', 'revalidate', 'revalidate same', 1_700, { verdict: 'same', grid: 'g/1' }),
  ];
  assert.deepEqual(causes(inside), ['gesture', 'gesture', 'gesture', 'gesture']);
  const outside = [
    rpc('GetGrid start', 5_000, { req: 'r9', id: 'g/1' }),
    rec('node', 'sourcecache', 'serve', 'serve cached', 5_001, { how: 'cached', grid: 'g/1' }),
    rpc('GetGrid ok', 5_002, { req: 'r9', id: 'g/1' }),
    rec('node', 'sourcecache', 'revalidate', 'revalidate same', 5_200, { verdict: 'same', grid: 'g/1' }),
  ];
  assert.deepEqual(causes(outside), [null, null, null, null]);
});

test("records other than the node's rpcs and source layers are not audited", () => {
  const lines = [rec('client', 'render', 'draw', 'draw', 9_000), rec('node', 'store', 'write', 'x', 9_000)];
  assert.equal(attribute(lines, windows).length, 0);
});

test('the budget fails on a rise and on an unrecorded fall', () => {
  assert.equal(budgetVerdict(3, 3), null);
  assert.match(budgetVerdict(4, 3)!, /rose from 3 to 4/);
  assert.match(budgetVerdict(2, 3)!, /fell from 3 to 2/);
});
