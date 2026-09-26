import * as fs from 'node:fs';

// One line of a dump: api/tracewire.Record as a suite reads it back. The
// node stamps seq and t, so a record the client made carries neither until it
// has been through the door.
export interface TraceLine {
  seq?: number;
  t?: number;
  origin: string;
  src: string;
  kind: string;
  msg: string;
  kv?: { [key: string]: string };
  cid?: string;
  ct?: number;
}

export function readDump(file: string): TraceLine[] {
  return fs
    .readFileSync(file, 'utf8')
    .split('\n')
    .filter((l) => l.trim() !== '')
    .map((l) => JSON.parse(l) as TraceLine);
}

// The request ids that name work on both sides of the door. kv.req is the
// join: the client stamps it on the call and both halves record it, so an id
// with a client rpc record and a node one is one gesture seen end to end.
export function joinedRequests(lines: TraceLine[]): string[] {
  const origins = new Map<string, Set<string>>();
  for (const r of lines) {
    if (r.kind !== 'rpc' || !r.kv?.req) continue;
    const seen = origins.get(r.kv.req) ?? new Set<string>();
    seen.add(r.origin);
    origins.set(r.kv.req, seen);
  }
  return [...origins.entries()]
    .filter(([, o]) => o.has('client') && o.has('node'))
    .map(([id]) => id);
}

// What the dump notice says, for a failure message that names the file the
// spec could not use.
export function describe(lines: TraceLine[]): string {
  const byOrigin = new Map<string, number>();
  for (const r of lines) byOrigin.set(r.origin, (byOrigin.get(r.origin) ?? 0) + 1);
  return [...byOrigin.entries()].map(([o, n]) => `${o}:${n}`).join(' ');
}

// dumpNow writes the node's ring once this client has posted every record it
// holds, and reads the file back. The page asks the door itself, so the dump
// is of the same session the spec drove; trace-dump.spec.ts owns the user's
// gesture for the same thing.
export async function dumpNow(window: any): Promise<{ cid: string; lines: TraceLine[] }> {
  const pending = () => window.evaluate(() => (window as any).__gridwellTest.trace().pending);
  const deadline = Date.now() + 20_000;
  while ((await pending()) > 0) {
    if (Date.now() > deadline) throw new Error('the client never posted its trace backlog');
    await window.waitForTimeout(100);
  }
  const { cid, file } = await window.evaluate(async () => {
    const r = await fetch('/trace/dump', { method: 'POST' });
    if (!r.ok) throw new Error(`dump: ${r.status} ${await r.text()}`);
    return { cid: (window as any).__gridwellTest.trace().cid, file: (await r.json()).path };
  });
  return { cid, lines: readDump(file) };
}
