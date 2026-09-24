import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';
import { TraceClient, TRACE_FLUSH_BATCH, TRACE_FLUSH_MS, TRACE_MAX_MSG, TRACE_PATH } from './trace';

// The fields of one line, as the node reads them back.
interface Row {
  origin: string;
  src: string;
  kind: string;
  msg: string;
  kv?: Record<string, string>;
  cid: string;
  ct: number;
}

const here = dirname(fileURLToPath(import.meta.url)); // apps/desktop/src/main
const repoRoot = resolve(here, '../../../..');

function goSource(path: string): string {
  return readFileSync(resolve(repoRoot, path), 'utf8');
}

// A literal trace.ts holds but does not export, read the way
// gesture-threshold.test.ts reads the other side of a drift lint.
function traceLiteral(re: RegExp): string {
  const m = re.exec(readFileSync(resolve(here, 'trace.ts'), 'utf8'));
  assert.ok(m, `no literal in trace.ts for ${re}`);
  return m![1];
}

// A clock a test drives, so the flush window is a value and not a wait.
function clock(start = 1727000000123): { now: () => number; advance: (ms: number) => void } {
  let t = start;
  return { now: () => t, advance: (ms) => (t += ms) };
}

function records(body: string): Row[] {
  assert.ok(body.endsWith('\n'), `the batch does not end in a newline: ${JSON.stringify(body)}`);
  return body
    .split('\n')
    .filter((l) => l !== '')
    .map((l) => JSON.parse(l) as Row);
}

// The line is the contract between two repositories' halves, so it is pinned
// to its bytes: the field order, the sorted kv, and the node's stamps absent.
test('a record is the wire line api/tracewire declares', () => {
  const c = new TraceClient({ cid: 'cid7abc', now: clock().now });
  c.emit({ src: 'webviews', kind: 'bounds', msg: 'pane p1', kv: { view: 'p1', h: '300', w: '400' } });
  const batch = c.pendingBatch();
  assert.ok(batch);
  assert.equal(
    batch!.body,
    '{"origin":"electron","src":"webviews","kind":"bounds","msg":"pane p1",' +
      '"kv":{"h":"300","view":"p1","w":"400"},"cid":"cid7abc","ct":1727000000123}\n',
  );
});

// An address in a message is read by a person, and a record with no kv sends
// none, so the line stays the same width as the Go client's.
test('a record with no kv omits the field and leaves the message as written', () => {
  const c = new TraceClient({ cid: 'cid7abc', now: clock().now });
  c.emit({ src: 'webviews', kind: 'nav-done', msg: 'https://x.test/a?b=1&c=2' });
  assert.equal(
    c.pendingBatch()!.body,
    '{"origin":"electron","src":"webviews","kind":"nav-done","msg":"https://x.test/a?b=1&c=2",' +
      '"cid":"cid7abc","ct":1727000000123}\n',
  );
});

// One process, one id, in the node's own shape, so a dump groups the lines a
// single run wrote.
test('a client mints one cid in the node id shape', () => {
  const a = new TraceClient();
  const b = new TraceClient();
  assert.match(a.cid, /^[a-z][a-z0-9]{6}$/);
  assert.notEqual(a.cid, b.cid);
});

// Nothing to say is nothing to post: an empty batch would cost a round trip
// per tick for the life of the process.
test('nothing is owed when nothing was emitted, or after an ack', async () => {
  const posted: string[] = [];
  const c = new TraceClient({ cid: 'cid7abc', post: async (b) => (posted.push(b), true) });
  assert.equal(c.pendingBatch(), null);
  await c.tick();
  assert.deepEqual(posted, []);
  c.emit({ src: 'main', kind: 'log', msg: 'one' });
  c.pendingBatch()!.ack();
  assert.equal(c.pendingCount(), 0);
  assert.equal(c.pendingBatch(), null);
});

// The two halves of the flush decision, mirroring client/trace.NeedFlush: a
// burst posts at once, and a lone record waits out the window rather than
// costing a round trip of its own.
test('a flush is due on a full batch or on the clock', async () => {
  const k = clock();
  const bodies: string[] = [];
  const c = new TraceClient({ cid: 'cid7abc', now: k.now, post: async (b) => (bodies.push(b), true) });
  c.emit({ src: 'main', kind: 'log', msg: 'one' });
  await c.tick();
  assert.deepEqual(bodies, [], 'a lone record inside the window posts nothing');
  k.advance(TRACE_FLUSH_MS);
  await c.tick();
  assert.equal(bodies.length, 1, 'the window elapsed with a record pending');

  bodies.length = 0;
  for (let i = 0; i < TRACE_FLUSH_BATCH; i++) c.emit({ src: 'main', kind: 'log', msg: `n${i}` });
  await c.tick();
  assert.equal(bodies.length, 1, 'a full batch posts whatever the clock says');
  assert.equal(records(bodies[0]).length, TRACE_FLUSH_BATCH);
});

// A post that never lands must not lose the records it carried: the door is
// unreliable and the trace is what explains the failure.
test('a failed post keeps its records for the next batch', async () => {
  const k = clock();
  const bodies: string[] = [];
  let ok = false;
  const c = new TraceClient({ cid: 'cid7abc', now: k.now, post: async (b) => (bodies.push(b), ok) });
  c.emit({ src: 'main', kind: 'log', msg: 'one' });
  k.advance(TRACE_FLUSH_MS);
  await c.tick();
  assert.equal(c.pendingCount(), 1, 'a refused batch is still owed');

  ok = true;
  c.emit({ src: 'main', kind: 'log', msg: 'two' });
  k.advance(TRACE_FLUSH_MS);
  await c.tick();
  assert.deepEqual(
    records(bodies[1]).map((r) => r.msg),
    ['one', 'two'],
    'the resend opens with the batch that failed',
  );
  assert.equal(c.pendingCount(), 0);
});

// Two posts of the same records would double every line in the dump, and the
// second would acknowledge what the first never delivered.
test('only one flush is in flight at a time', async () => {
  const k = clock();
  let posts = 0;
  let release = (): void => {};
  const c = new TraceClient({
    cid: 'cid7abc',
    now: k.now,
    post: async () => {
      posts++;
      await new Promise<void>((r) => (release = r));
      return true;
    },
  });
  c.emit({ src: 'main', kind: 'log', msg: 'one' });
  k.advance(TRACE_FLUSH_MS);
  const first = c.tick();
  c.emit({ src: 'main', kind: 'log', msg: 'two' });
  k.advance(TRACE_FLUSH_MS);
  await c.tick();
  assert.equal(posts, 1, 'the second tick posted while the first was in flight');
  release();
  await first;
  assert.equal(c.pendingCount(), 1, 'the record emitted in flight was not acknowledged by that post');
});

// The ring is the bound on an unreachable node. What it drops is a hole in the
// story, so the hole is told: one record, with the count.
test('a wrap drops the oldest unacknowledged records and says so', () => {
  const k = clock();
  const c = new TraceClient({ cid: 'cid7abc', capacity: 4, now: k.now });
  for (const m of ['one', 'two', 'three', 'four', 'five', 'six']) c.emit({ src: 'main', kind: 'log', msg: m });
  const recs = records(c.pendingBatch()!.body);
  assert.equal(
    recs.filter((r) => r.msg === 'one' || r.msg === 'two').length,
    0,
    'a wrapped ring still claims the records it evicted',
  );
  const drops = recs.filter((r) => r.src === 'trace');
  assert.equal(drops.length, 1, 'one loss record per batch');
  assert.equal(drops[0].kind, 'drop');
  assert.equal(drops[0].kv?.n, '2');
  assert.equal(drops[0].ct, k.now(), 'the loss carries the clock of the emit that evicted');
});

// A record the node kept is not a loss, however far the ring has moved past
// it, or every quiet process would report drops it never had.
test('a wrap over acknowledged records is no loss', () => {
  const c = new TraceClient({ cid: 'cid7abc', capacity: 4 });
  for (const m of ['one', 'two', 'three', 'four']) c.emit({ src: 'main', kind: 'log', msg: m });
  c.pendingBatch()!.ack();
  for (const m of ['five', 'six', 'seven']) c.emit({ src: 'main', kind: 'log', msg: m });
  assert.deepEqual(
    records(c.pendingBatch()!.body)
      .filter((r) => r.src === 'trace')
      .map((r) => r.msg),
    [],
  );
});

// A message past the cap is cut, never dropped: that the thing happened is the
// part worth keeping, and the cut lands on a rune boundary so the line still
// reads.
test('a long message is cut on a rune boundary', () => {
  const msg = 'é'.repeat(TRACE_MAX_MSG); // two bytes each
  const c = new TraceClient({ cid: 'cid7abc' });
  c.emit({ src: 'webviews', kind: 'nav-done', msg });
  const got = records(c.pendingBatch()!.body)[0].msg;
  assert.ok(Buffer.byteLength(got, 'utf8') <= TRACE_MAX_MSG);
  assert.ok(got.length > 0 && msg.startsWith(got));
  assert.equal(got, 'é'.repeat(TRACE_MAX_MSG / 2), 'the cut split a rune');
});

// The caller's map is the caller's: a map reused for the next record cannot
// rewrite the last one.
test('emit copies the caller kv', () => {
  const c = new TraceClient({ cid: 'cid7abc' });
  const kv = { view: 'p1' };
  c.emit({ src: 'webviews', kind: 'bounds', msg: 'x', kv });
  kv.view = 'p2';
  assert.equal(records(c.pendingBatch()!.body)[0].kv?.view, 'p1');
});

// ── Drift lints across the language seam ───────────────────────────────────
// api/tracewire owns the door and the record, client/trace the ring, and
// client/cadence the flush window. The main process has no Go in it, so it
// spells each again; a drifted value posts to a path that 404s, sends an
// origin the node rewrites, or holds records past the window with nothing to
// say why.

test('the trace door constants match api/tracewire', () => {
  const src = goSource('api/tracewire/tracewire.go');
  assert.ok(src.includes(`Path = "${TRACE_PATH}"`), `api/tracewire no longer names the path ${TRACE_PATH}`);
  const origin = traceLiteral(/TRACE_ORIGIN = '([^']+)'/);
  assert.ok(
    src.includes(`OriginElectron = "${origin}"`),
    `api/tracewire no longer names the origin ${origin}; the node would rewrite it to "client"`,
  );
  assert.ok(src.includes(`MaxMsg = ${TRACE_MAX_MSG}`), 'trace.ts TRACE_MAX_MSG drifted from tracewire.MaxMsg (the owner)');
});

test('the ring and batch sizes match client/trace', () => {
  const src = goSource('client/trace/trace.go');
  assert.ok(
    src.includes(`DefaultCapacity = ${traceLiteral(/TRACE_RING = (\d+)/)}`),
    'trace.ts TRACE_RING drifted from client/trace.DefaultCapacity (the owner)',
  );
  assert.ok(
    goSource('client/trace/flush.go').includes(`FlushBatch = ${TRACE_FLUSH_BATCH}`),
    'trace.ts TRACE_FLUSH_BATCH drifted from client/trace.FlushBatch (the owner)',
  );
});

test('the flush window matches client/cadence', () => {
  const m = /TraceFlushMs = (\d+)/.exec(goSource('client/cadence/cadence.go'));
  assert.ok(m, 'client/cadence no longer declares TraceFlushMs');
  assert.equal(
    TRACE_FLUSH_MS,
    Number(m![1]),
    'trace.ts TRACE_FLUSH_MS drifted from cadence.TraceFlushMs (the owner)',
  );
});
