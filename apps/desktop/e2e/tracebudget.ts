import type { TraceLine } from './trace';

// The trace budget's attribution: every node rpc record and every sourcecache
// or pluginhost work record in a dump is owed to a gesture, an event the node
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
  return (r.src === 'router' && r.kind === 'rpc') || r.src === 'sourcecache' || r.src === 'pluginhost';
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
    } else if (r.src === 'pluginhost' && (r.kind === 'changed' || r.kind === 'health')) {
      cause = 'event';
    } else if (r.src === 'sourcecache' && r.kind === 'dark') {
      cause = 'event';
    } else if (r.src === 'pluginhost' && r.kind === 'watch') {
      cause = viewBefore(lines, r) ? 'view' : null;
    } else if (r.src === 'sourcecache' && r.kind === 'revalidate') {
      cause = servedBefore(r, out);
    }
    cause = cause ?? enclosing(r);
    byKey.set(r.seq ?? 0, cause);
    out.push({ rec: r, cause });
  }
  return out;
}

// viewBefore is a SetView or Subscribe the node heard within CAUSE_MS before
// r: a Watch opens and closes on what the view shows.
function viewBefore(lines: TraceLine[], r: TraceLine): boolean {
  return lines.some(
    (v) =>
      v.origin === 'node' &&
      v.src === 'router' &&
      v.kind === 'rpc' &&
      VIEW_VERBS.has(verbOf(v)) &&
      (v.seq ?? 0) < (r.seq ?? 0) &&
      (r.t ?? 0) - (v.t ?? 0) <= CAUSE_MS,
  );
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
