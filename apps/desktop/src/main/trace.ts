// The main process's half of the always-on trace: a ring of records posted to
// the node's trace door, so one dump tells what the native layer did beside
// what the canvas and the node did. api/tracewire owns the wire — the path,
// the record and the cap — and client/trace owns the ring's semantics; this is
// their spelling for a process with no Go in it, and trace.test.ts pins both
// copies against their owners.

// See api/tracewire.Path.
export const TRACE_PATH = '/trace';

// See api/tracewire.OriginElectron: the one origin the node lets this process
// claim. Anything else it rewrites to "client".
const TRACE_ORIGIN = 'electron';

// See api/tracewire.MaxMsg. The node truncates too; doing it here keeps a
// runaway message out of the ring as well as off the wire.
export const TRACE_MAX_MSG = 1024;

// See client/trace.DefaultCapacity.
const TRACE_RING = 2000;

// See client/trace.FlushBatch.
export const TRACE_FLUSH_BATCH = 200;

// See client/cadence.TraceFlushMs.
export const TRACE_FLUSH_MS = 1000;

// A loss is itself a record, under the trace's own name; see client/trace.
const DROP_SRC = 'trace';
const DROP_KIND = 'drop';

// What an emitter says. The record's other fields are this process's to
// stamp, so no caller can spell them differently.
interface TraceEvent {
  src: string;
  kind: string;
  msg: string;
  kv?: Record<string, string>;
}

// One line of the trace, in api/tracewire.Record's field order, which the
// byte-exact test pins. The node stamps seq and t on receipt.
interface TraceRecord {
  origin: string;
  src: string;
  kind: string;
  msg: string;
  kv?: Record<string, string>;
  cid: string;
  ct: number;
}

// The door, injected so a test never reaches the network. True means the node
// kept the batch; anything else leaves it pending for the next one.
type TracePost = (body: string) => Promise<boolean>;

interface TraceOptions {
  cid?: string;
  capacity?: number;
  post?: TracePost;
  now?: () => number;
}

// TraceClient is client/trace.Client in TypeScript: the same ring, the same
// acknowledgement arithmetic, the same loss record, so a dump reads the same
// whichever client wrote the line.
export class TraceClient {
  readonly cid: string;
  private readonly ring: (TraceRecord | undefined)[];
  private readonly now: () => number;
  private post: TracePost | null;
  // A serial counts every record ever emitted, so an acknowledgement survives
  // a wrap: base is the serial of the oldest record held, and the range still
  // owed is always [acked, base+count).
  private first = 0;
  private count = 0;
  private base = 0;
  private acked = 0;
  private dropped = 0;
  private lastCT = 0;
  private lastFlush: number;
  private inFlight = false;

  constructor(o: TraceOptions = {}) {
    this.cid = o.cid ?? newCID();
    this.ring = new Array<TraceRecord | undefined>(o.capacity && o.capacity > 0 ? o.capacity : TRACE_RING);
    this.now = o.now ?? Date.now;
    this.post = o.post ?? null;
    this.lastFlush = this.now();
  }

  // sendWith arms the door once the node's origin is known. What was emitted
  // before that stays pending, so boot rides the first batch.
  sendWith(post: TracePost): void {
    this.post = post;
  }

  emit(ev: TraceEvent): void {
    this.lastCT = this.now();
    this.write(ev, this.lastCT);
  }

  pendingCount(): number {
    return this.base + this.count - this.acked;
  }

  // needFlush is client/trace.NeedFlush: a burst is a gesture going wrong,
  // which is the moment the node most needs to hear, and nothing pending is
  // nothing to post.
  needFlush(): boolean {
    const pending = this.pendingCount();
    if (pending <= 0) return false;
    return pending >= TRACE_FLUSH_BATCH || this.now() - this.lastFlush >= TRACE_FLUSH_MS;
  }

  // tick posts when one is due. One flush is in flight at a time, because two
  // would send the same records twice and acknowledge each other's.
  async tick(): Promise<void> {
    if (this.inFlight || !this.post || !this.needFlush()) return;
    const batch = this.pendingBatch();
    if (!batch) return;
    this.inFlight = true;
    this.lastFlush = this.now();
    let ok = false;
    try {
      ok = await this.post(batch.body);
    } catch {
      // The door is unreachable; the records stay pending and the ring is the
      // bound on that memory.
    } finally {
      this.inFlight = false;
    }
    if (ok) batch.ack();
  }

  // pendingBatch is every unacknowledged record as JSON lines, and the ack to
  // call once the door has answered.
  pendingBatch(): { body: string; ack: () => void } | null {
    if (this.dropped > 0) {
      // The evictions since the last batch are one record, which can itself
      // evict another; that loss rides the next batch. It carries the clock of
      // the emit that discovered it.
      const n = this.dropped;
      this.dropped = 0;
      this.write({ src: DROP_SRC, kind: DROP_KIND, msg: 'records dropped before the node saw them', kv: { n: String(n) } }, this.lastCT);
    }
    const from = this.acked;
    const end = this.base + this.count;
    if (from === end) return null;
    let body = '';
    for (let s = from; s < end; s++) {
      body += JSON.stringify(this.ring[(this.first + (s - this.base)) % this.ring.length]) + '\n';
    }
    // The ack closes over this batch's end, so a record emitted while the post
    // is in flight stays pending.
    return {
      body,
      ack: () => {
        if (end > this.acked) this.acked = end;
      },
    };
  }

  private write(ev: TraceEvent, ct: number): void {
    if (this.count === this.ring.length) {
      // An evicted record the node never kept is a hole the next batch tells.
      if (this.acked <= this.base) {
        this.dropped++;
        this.acked = this.base + 1;
      }
      this.first = (this.first + 1) % this.ring.length;
      this.base++;
      this.count--;
    }
    const origin = TRACE_ORIGIN;
    const { src, kind } = ev;
    const msg = truncate(ev.msg);
    // Two literals rather than one built up, because a line's bytes are the
    // contract: kv sits between msg and cid, and is absent when empty.
    const kv = sortedKV(ev.kv);
    const rec: TraceRecord = kv
      ? { origin, src, kind, msg, kv, cid: this.cid, ct }
      : { origin, src, kind, msg, cid: this.cid, ct };
    this.ring[(this.first + this.count) % this.ring.length] = rec;
    this.count++;
  }
}

// The one ring of this process, package-level like internal/trace.Default and
// for the same reason: it holds no fact of the user's, so nothing reads it
// back and deleting it loses nothing.
const mainRing = new TraceClient();

// trace is what every emit site calls. A record costs nothing until the door
// is armed, so a site need not know whether boot has reached it yet.
export function trace(ev: TraceEvent): void {
  mainRing.emit(ev);
  void mainRing.tick();
}

// logLine is main's own console output: the sidecar's lines are already in the
// node's ring through its log capture, and these are the ones it never saw.
export function logLine(level: 'log' | 'error', line: string): void {
  if (level === 'error') console.error(line);
  else console.log(line);
  trace({ src: 'main', kind: 'log', msg: line });
}

// startTrace arms the door and keeps the clock half of the flush decision
// running, so a lone record posts within the window instead of waiting for
// company that never comes.
export function startTrace(post: TracePost): void {
  mainRing.sendWith(post);
  setInterval(() => void mainRing.tick(), TRACE_FLUSH_MS);
}

// truncate cuts on a rune boundary, so a long message is still text.
function truncate(msg: string): string {
  const b = Buffer.from(msg, 'utf8');
  if (b.length <= TRACE_MAX_MSG) return msg;
  let cut = TRACE_MAX_MSG;
  // 10xxxxxx is a continuation byte, so the rune it belongs to is unfinished.
  while (cut > 0 && (b[cut] & 0xc0) === 0x80) cut--;
  return b.subarray(0, cut).toString('utf8');
}

// sortedKV keeps the caller's map out of the ring: a record says what was true
// when it was emitted.
function sortedKV(kv: Record<string, string> | undefined): Record<string, string> | null {
  if (!kv) return null;
  const keys = Object.keys(kv).sort();
  if (keys.length === 0) return null;
  const out: Record<string, string> = {};
  for (const k of keys) out[k] = kv[k];
  return out;
}

// The node's id shape (api/idshape): 7 lowercase base36 characters with a
// leading letter. One per process, minted at start, so a dump groups the lines
// one run wrote.
function newCID(): string {
  const letters = 'abcdefghijklmnopqrstuvwxyz';
  const alnum = letters + '0123456789';
  let out = letters[Math.floor(Math.random() * letters.length)];
  for (let i = 1; i < 7; i++) out += alnum[Math.floor(Math.random() * alnum.length)];
  return out;
}
