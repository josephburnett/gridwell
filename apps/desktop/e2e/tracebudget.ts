import type { TraceLine } from './trace';

// The trace budget's attribution: every node rpc record, every sourcecache
// work record and every files health record in a dump is owed to a gesture, an event the node
// or client was told, a view the node was told, or a decision CLAUDE.md names.
// What is owed to none of them was done for nobody or asked on a clock, which
// two promises forbid ("told, never asked"; "nothing is done for nobody"), and
// its count is the trace-budget row of test/boundary/ratchet.txt.

// A span of the script the spec marks as one gesture, on the host clock the
// node stamps t with. The quiet wait is not one.
export interface GestureWindow {
  name: string;
  start: number;
  end: number;
}

export type Cause =
  | 'gesture'
  | 'event'
  | 'view'
  | 'decision: prefetch walk (2026-10-06)'
  | 'decision: stream-gap resync';

export interface Verdict {
  rec: TraceLine;
  cause: Cause | null;
}

// How long after a client record the call it caused may start: an event's
// refetch or a resync's read is issued in the same turn of the client's loop.
export const CAUSE_MS = 2_000;

const VIEW_VERBS = new Set(['SetView', 'Subscribe']);

// audited is the work the budget counts: the node's rpc records and its
// source layers' work.
export function audited(r: TraceLine): boolean {
  if (r.origin !== 'node') return false;
  return (r.src === 'router' && r.kind === 'rpc') || r.src === 'sourcecache' || r.src === 'files';
}

function verbOf(r: TraceLine): string {
  return r.msg.split(' ')[0];
}

function inWindow(t: number | undefined, windows: GestureWindow[]): boolean {
  return t !== undefined && windows.some((w) => t >= w.start && t <= w.end);
}

// The node rpc spans, start to end by seq. A span is keyed by verb, req and
// id; two open at once under one key pair first in, first out. Subscribe is
// left out: it is open for the whole session and encloses nothing it caused.
interface Span {
  start: TraceLine;
  endSeq: number;
}

function spans(lines: TraceLine[]): Span[] {
  const open = new Map<string, TraceLine[]>();
  const out: Span[] = [];
  for (const r of lines) {
    if (r.origin !== 'node' || r.src !== 'router' || r.kind !== 'rpc') continue;
    const verb = verbOf(r);
    if (verb === 'Subscribe') continue;
    const key = `${verb}|${r.kv?.req ?? ''}|${r.kv?.id ?? ''}`;
    if (r.msg === `${verb} start`) {
      open.set(key, [...(open.get(key) ?? []), r]);
      continue;
    }
    const q = open.get(key);
    const start = q?.shift();
    if (start) out.push({ start, endSeq: r.seq ?? 0 });
  }
  for (const q of open.values()) for (const start of q) out.push({ start, endSeq: Infinity });
  return out;
}

// clientCause is what the client record nearest before t, within CAUSE_MS,
// says caused a call it made: an event it received, a resync after a stream
// gap, or nothing.
function clientCause(events: TraceLine[], t: number | undefined): Cause | null {
  if (t === undefined) return null;
  let best: TraceLine | null = null;
  for (const r of events) {
    if (r.t === undefined) continue;
    if (r.t <= t && t - r.t <= CAUSE_MS && (!best || r.t > (best.t ?? 0))) best = r;
  }
  if (!best) return null;
  return best.kind === 'resync' ? 'decision: stream-gap resync' : 'event';
}

// attribute names a cause for every audited record of lines, oldest first.
export function attribute(lines: TraceLine[], windows: GestureWindow[]): Verdict[] {
  const byKey = new Map<number, Cause | null>();
  const events = lines.filter((r) => r.origin === 'client' && r.src === 'events');
  const askedAt = new Map<string, number | undefined>();
  for (const c of lines) {
    if (c.origin === 'client' && c.kind === 'rpc' && c.kv?.req && !askedAt.has(c.kv.req)) askedAt.set(c.kv.req, c.t);
  }
  const rpcCause = (r: TraceLine): Cause | null => {
    if (VIEW_VERBS.has(verbOf(r))) return 'view';
    const req = r.kv?.req ?? '';
    // The client's own record of the call, by the request id both halves
    // carry, dates it on the client's clock.
    const told = clientCause(events, (req === '' ? undefined : askedAt.get(req)) ?? r.t);
    if (req !== '' && told) return told;
    if (!inWindow(r.t, windows)) return null;
    if (req !== '') return 'gesture';
    // A call with no request id is not the client's; in a gesture it is
    // owed to the gesture only when the client or Electron wrote in it too.
    const w = windows.find((w) => r.t! >= w.start && r.t! <= w.end)!;
    const wrote = lines.some((c) => (c.origin === 'client' || c.origin === 'electron') && c.t !== undefined && c.t >= w.start && c.t <= w.end);
    return wrote ? 'gesture' : null;
  };
  const all = spans(lines);
  const enclosing = (r: TraceLine): Cause | null => {
    const seq = r.seq ?? 0;
    for (const s of all) {
      if ((s.start.seq ?? 0) < seq && seq < s.endSeq) {
        const c = byKey.get(s.start.seq ?? 0);
        if (c) return c;
      }
    }
    return null;
  };
  const out: Verdict[] = [];
  for (const r of lines) {
    if (!audited(r)) continue;
    let cause: Cause | null = null;
    if (r.src === 'router') {
      cause = rpcCause(r);
    } else if (r.src === 'sourcecache' && r.kind === 'prefetch') {
      cause = 'decision: prefetch walk (2026-10-06)';
    } else if ((r.src === 'files' && r.kind === 'health') || (r.src === 'sourcecache' && r.kind === 'dark')) {
      cause = 'event';
    } else if (r.src === 'sourcecache' && r.kind === 'revalidate') {
      cause = servedBefore(r, out);
    }
    cause = cause ?? enclosing(r);
    byKey.set(r.seq ?? 0, cause);
    out.push({ rec: r, cause });
  }
  return out;
}

// servedBefore is the cause of the cached serve of the same grid a
// revalidation runs behind.
function servedBefore(r: TraceLine, done: Verdict[]): Cause | null {
  for (let i = done.length - 1; i >= 0; i--) {
    const d = done[i].rec;
    if (d.src === 'sourcecache' && d.kind === 'serve' && d.kv?.how === 'cached' && d.kv?.grid === r.kv?.grid) {
      return done[i].cause;
    }
  }
  return null;
}

// One unattributed record as the spec prints it and attaches it.
export interface Unattributed {
  seq: number;
  src: string;
  kind: string;
  msg: string;
  id: string;
}

export function unattributed(verdicts: Verdict[]): Unattributed[] {
  return verdicts
    .filter((v) => v.cause === null)
    .map(({ rec }) => ({
      seq: rec.seq ?? 0,
      src: rec.src,
      kind: rec.kind,
      msg: rec.msg,
      id: rec.kv?.id ?? rec.kv?.grid ?? rec.kv?.grids ?? '',
    }));
}

// budgetVerdict is the ratchet's rule on the recorded budget: a rise fails,
// and so does a fall the file does not record, so the number stays exact.
// null is a pass.
export function budgetVerdict(count: number, recorded: number): string | null {
  if (count > recorded) {
    return `unattributed work rose from ${recorded} to ${count}: attribute each new record to a gesture, an event, a view or a decision in CLAUDE.md, or stop the work`;
  }
  if (count < recorded) {
    return `unattributed work fell from ${recorded} to ${count}: record ${count} in apps/desktop/e2e/trace-budget.json and test/boundary/ratchet.txt in this commit`;
  }
  return null;
}

// The session audits: what a dump of ordinary use must not hold, read by the
// specs in place of a person reading it. Each answers the offending records,
// so a failure prints them.

const READ_VERBS = new Set(['GetGrid', 'GetTile', 'GetTilePreview', 'ReadContent', 'Handshake']);

interface ClientCall {
  verb: string;
  id: string;
  req: string;
  start: number;
  end: number;
  outcome: string;
  code: string;
}

// clientCalls pairs the client's rpc start and end records by request id, on
// the node's clock. A call still open at the dump has end Infinity.
export function clientCalls(lines: TraceLine[]): ClientCall[] {
  const open = new Map<string, ClientCall>();
  const out: ClientCall[] = [];
  for (const r of lines) {
    if (r.origin !== 'client' || r.kind !== 'rpc' || !r.kv?.req || r.t === undefined) continue;
    const [verb, ...rest] = r.msg.split(' ');
    const phase = rest.join(' ');
    if (phase === 'start') {
      const c = { verb, id: r.kv.id ?? '', req: r.kv.req, start: r.t, end: Infinity, outcome: '', code: '' };
      open.set(r.kv.req, c);
      out.push(c);
      continue;
    }
    const c = open.get(r.kv.req);
    if (!c) continue;
    open.delete(r.kv.req);
    c.end = r.t;
    c.outcome = phase.split(':')[0];
    c.code = r.kv.code ?? '';
  }
  return out;
}

// refusedCalls is every call the node answered with an error. The client's
// own cancel is no refusal (clientsync.OutcomeAbandoned), and a dead link is
// a state (gwerr.DeadLink), so neither counts.
export function refusedCalls(lines: TraceLine[]): ClientCall[] {
  return clientCalls(lines).filter((c) => c.outcome === 'error' && c.code !== 'canceled');
}

// errorNotices is every notice of severity error the client raised.
export function errorNotices(lines: TraceLine[]): TraceLine[] {
  return lines.filter((r) => r.origin === 'client' && r.kind === 'notice' && r.kv?.sev === 'error');
}

// droppedPanes is every notice that a workspace's layout lost a pane on its
// way in or out (the layout:<tile> notices).
export function droppedPanes(lines: TraceLine[]): TraceLine[] {
  return lines.filter((r) => r.origin === 'client' && r.kind === 'notice' && r.src.startsWith('layout:'));
}

// What can change what a read answers: on the client an event it was told,
// a gesture, a navigation, a pane change, a drop or a write it made; on the
// node any write, whoever asked it, and a source's health.
function changes(r: TraceLine): boolean {
  const verb = r.msg.split(' ')[0];
  const write = r.kind === 'rpc' && !READ_VERBS.has(verb) && verb !== 'SetView' && verb !== 'Subscribe';
  if (r.origin === 'node') return (r.src === 'router' && write) || r.src === 'connection' || r.src === 'files';
  if (r.origin !== 'client') return false;
  return ['events', 'gesture', 'nav', 'pane', 'drag', 'text', 'framing'].includes(r.src) || write;
}

// repeatedReads is every read of one id asked again after the last one was
// answered, with nothing since the last was asked that could change its
// answer: a write the node finished while serving it counts, since the
// first answer may predate it.
export function repeatedReads(lines: TraceLine[]): ClientCall[] {
  const calls = clientCalls(lines).filter((c) => READ_VERBS.has(c.verb) && c.id !== '');
  const marks = lines.filter(changes).map((r) => r.t ?? 0);
  const last = new Map<string, ClientCall>();
  const out: ClientCall[] = [];
  for (const c of calls.sort((a, b) => a.start - b.start)) {
    const key = `${c.verb}|${c.id}`;
    const prev = last.get(key);
    if (prev && prev.outcome === 'ok' && prev.end <= c.start && !marks.some((t) => t >= prev.start && t <= c.start)) out.push(c);
    last.set(key, c);
  }
  return out;
}

// maxInFlight is, per id, the most calls of verb the client had open at once.
export function maxInFlight(lines: TraceLine[], verb: string): Map<string, number> {
  const edges: Array<[number, number, string]> = [];
  for (const c of clientCalls(lines)) {
    if (c.verb !== verb) continue;
    edges.push([c.start, 1, c.id], [c.end, -1, c.id]);
  }
  edges.sort((a, b) => a[0] - b[0] || a[1] - b[1]);
  const now = new Map<string, number>();
  const most = new Map<string, number>();
  for (const [, d, id] of edges) {
    const n = (now.get(id) ?? 0) + d;
    now.set(id, n);
    most.set(id, Math.max(most.get(id) ?? 0, n));
  }
  return most;
}

// roundTrips is the longest chain of reads, each asked after the one before
// it was answered, that started at or after from and were answered by to: the
// serial round trips a paint at to waited on.
export function roundTrips(lines: TraceLine[], from: number, to: number): number {
  const reads = clientCalls(lines)
    .filter((c) => READ_VERBS.has(c.verb) && c.start >= from && c.end <= to)
    .sort((a, b) => a.start - b.start);
  const depth: number[] = [];
  reads.forEach((c, i) => {
    let d = 1;
    for (let j = 0; j < i; j++) if (reads[j].end <= c.start) d = Math.max(d, depth[j] + 1);
    depth.push(d);
  });
  return Math.max(0, ...depth);
}
