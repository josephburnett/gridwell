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

// The origins that wrote a boot record (api/tracewire.KindBoot), each naming
// the build behind every record after it.
export function bootOrigins(lines: TraceLine[]): string[] {
  return [...new Set(lines.filter((r) => r.kind === 'boot').map((r) => r.origin))].sort();
}

// What the dump notice says, for a failure message that names the file the
// spec could not use.
export function describe(lines: TraceLine[]): string {
  const byOrigin = new Map<string, number>();
  for (const r of lines) byOrigin.set(r.origin, (byOrigin.get(r.origin) ?? 0) + 1);
  return [...byOrigin.entries()].map(([o, n]) => `${o}:${n}`).join(' ');
}

// dumpViaDoor takes a dump over the web door on the given cookie and reads the
// file it names. A dump writes the ring to a file and clears nothing, so a
// poll may take it as often as it likes.
export async function dumpViaDoor(origin: string, token: string): Promise<TraceLine[]> {
  const res = await fetch(origin + '/trace/dump', {
    method: 'POST',
    headers: { Cookie: `gridwell_auth=${token}` },
  });
  if (!res.ok) throw new Error(`POST /trace/dump = ${res.status} ${await res.text()}`);
  const { path } = (await res.json()) as { path: string; records: number };
  return readDump(path);
}
