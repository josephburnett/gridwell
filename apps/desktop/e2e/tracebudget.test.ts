import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { TraceLine } from './trace';
import {
  askedAtOnce, attribute, budgetVerdict, CAUSE_MS, droppedPanes, errorNotices, eventToPaint, GestureWindow, maxInFlight, queuedCalls,
  refusedCalls, repeatedReads, roundTrips, unattributed,
} from './tracebudget';

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

test("the source layers: a walk is the decision's, a health change the event's", () => {
  const lines = [
    rec('node', 'sourcecache', 'prefetch', 'prefetch start', 9_000),
    rec('node', 'files', 'health', 'dark true, told true', 9_000),
    rec('node', 'sourcecache', 'dark', 'far dark', 9_000),
  ];
  assert.deepEqual(causes(lines), ['decision: prefetch walk (2026-10-06)', 'event', 'event']);
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

const call = (verb: string, req: string, start: number, end: number, end_: string, kv: Record<string, string> = {}) => [
  rec('client', 'rpc', 'rpc', `${verb} start`, start, { req, ...kv }),
  rec('client', 'rpc', 'rpc', `${verb} ${end_}`, end, { req, ...kv }),
];

test('a refusal counts; the client cancelling its own read and a dead link do not', () => {
  const lines = [
    ...call('MoveTile', 'a', 1, 2, 'error: failed_precondition: stale', { id: 't/1', code: 'failed_precondition' }),
    ...call('GetGrid', 'b', 3, 4, 'error: canceled: context canceled', { id: 'g/1', code: 'canceled' }),
    ...call('GetGrid', 'c', 5, 6, 'dead', { id: 'g/2' }),
  ];
  assert.deepEqual(refusedCalls(lines).map((c) => c.req), ['a']);
});

test('error notices and layout notices are found by severity and source', () => {
  const lines = [
    rec('client', 'grid:g', 'notice', 'grid unavailable', 1, { sev: 'error' }),
    rec('client', 'trace', 'notice', 'logs dumped', 2, { sev: 'info' }),
    rec('client', 'layout:w/1', 'notice', 'it will reopen at home', 3, { sev: 'info' }),
  ];
  assert.deepEqual(errorNotices(lines).map((r) => r.src), ['grid:g']);
  assert.deepEqual(droppedPanes(lines).map((r) => r.src), ['layout:w/1']);
});

test('a read asked again with nothing between is repeated; an event between excuses it', () => {
  const twice = [...call('GetGrid', 'a', 1, 2, 'ok', { id: 'g/1' }), ...call('GetGrid', 'b', 5, 6, 'ok', { id: 'g/1' })];
  assert.deepEqual(repeatedReads(twice).map((c) => c.req), ['b']);
  const told = [
    ...call('GetGrid', 'a', 1, 2, 'ok', { id: 'g/1' }),
    rec('client', 'events', 'recv', 'grid changed', 3, { id: 'g/1' }),
    ...call('GetGrid', 'b', 5, 6, 'ok', { id: 'g/1' }),
  ];
  assert.deepEqual(repeatedReads(told), []);
  const written = [
    ...call('GetGrid', 'a', 1, 2, 'ok', { id: 'g/1' }),
    rec('node', 'router', 'rpc', 'CreateTile start', 3, { id: 'g/1' }),
    ...call('GetGrid', 'b', 5, 6, 'ok', { id: 'g/1' }),
  ];
  assert.deepEqual(repeatedReads(written), [], "another writer's write on the node excuses it");
  const overlapping = [...call('GetGrid', 'a', 1, 6, 'ok', { id: 'g/1' }), ...call('GetGrid', 'b', 2, 7, 'ok', { id: 'g/1' })];
  assert.deepEqual(repeatedReads(overlapping), []);
});

test('in-flight is counted per id', () => {
  const lines = [
    ...call('WriteContent', 'a', 1, 4, 'ok', { id: 't/1' }),
    ...call('WriteContent', 'b', 4, 6, 'ok', { id: 't/1' }),
    ...call('WriteContent', 'c', 2, 3, 'ok', { id: 't/2' }),
    ...call('WriteContent', 'd', 2, 5, 'ok', { id: 't/2' }),
  ];
  const m = maxInFlight(lines, 'WriteContent');
  assert.equal(m.get('t/1'), 1);
  assert.equal(m.get('t/2'), 2);
});

test('round trips are the longest chain of reads each asked after the last was answered', () => {
  const lines = [
    ...call('GetGrid', 'a', 10, 20, 'ok', { id: 'g/1' }),
    ...call('ReadContent', 'b', 25, 30, 'ok', { id: 't/1' }),
    ...call('GetTilePreview', 'c', 12, 40, 'ok', { id: 't/2' }),
    ...call('GetGrid', 'd', 31, 35, 'ok', { id: 'g/2' }),
  ];
  assert.equal(roundTrips(lines, 10, 50), 3);
  assert.equal(roundTrips(lines, 10, 32), 2);
  assert.equal(roundTrips(lines, 100, 200), 0);
});

const crec = (src: string, kind: string, msg: string, ct: number, kv?: Record<string, string>): TraceLine => ({
  ...rec('client', src, kind, msg, ct, kv),
  ct,
});

test("an event's paint is the first draw begun after it was folded in, on the client's clock", () => {
  const lines = [
    crec('render', 'draw', 'pointer move', 100, { ms: '8' }),
    crec('events', 'apply', 'tile changed', 105, { id: 't/1' }),
    crec('render', 'draw', 'event applied', 130, { ms: '9.5' }),
    crec('events', 'apply', 'grid changed', 200, { id: 'g/1' }),
  ];
  assert.deepEqual(eventToPaint(lines), [
    { id: 't/1', msg: 'tile changed', ms: 25 },
    { id: 'g/1', msg: 'grid changed', ms: Infinity },
  ]);
});

test('a call waits from the client sending it to the node starting it, beside every call then open', () => {
  const node = (msg: string, t: number, req: string) => rec('node', 'router', 'rpc', msg, t, { req });
  const lines = [
    rec('client', 'rpc', 'rpc', 'Subscribe start', 0, { req: 's' }),
    ...call('GetTilePreview', 'a', 10, 900, 'ok', { id: 't/1' }),
    ...call('GetTilePreview', 'b', 11, 900, 'ok', { id: 't/2' }),
    ...call('MoveTile', 'c', 20, 300, 'ok', { id: 't/3' }),
    node('GetTilePreview start', 12, 'a'),
    node('GetTilePreview start', 14, 'b'),
    node('MoveTile start', 280, 'c'),
  ];
  assert.deepEqual(
    queuedCalls(lines).map((q) => [q.verb, q.waitMs, q.inFlight]),
    [
      ['GetTilePreview', 2, 1],
      ['GetTilePreview', 3, 2],
      ['MoveTile', 260, 3],
    ],
  );
});

test('calls asked at once are the longest run of starts each within ONCE_MS of the last', () => {
  const lines = [
    crec('rpc', 'rpc', 'Subscribe start', 0, { req: 's' }),
    crec('rpc', 'rpc', 'GetGrid start', 1, { req: 'a' }),
    crec('rpc', 'rpc', 'GetTilePreview start', 100, { req: 'b' }),
    crec('rpc', 'rpc', 'GetTilePreview start', 102, { req: 'c' }),
    crec('rpc', 'rpc', 'GetTilePreview start', 104, { req: 'd' }),
    crec('rpc', 'rpc', 'GetTilePreview ok', 105, { req: 'b' }),
    crec('rpc', 'rpc', 'SetView start', 200, { req: 'e' }),
  ];
  assert.equal(askedAtOnce(lines), 3);
  assert.equal(askedAtOnce([]), 0);
});
