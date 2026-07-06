// Per-plugin session sync. The plugin is the session boundary: each plugin's
// url tiles render in a durable partition (persist:plugin-<uuid>), and the
// session of record lives in the plugin's DB, moved over the Gridwell interface
// via GET/PUT /session/<uuid> on the sidecar.
//
// On entering a plugin's space the host hydrates the partition from the DB blob
// (pull the session down); on flush/ascent it dehydrates back (capture).
//
// The blob is a v2 envelope: cookies (portable — they inject through the
// public API into any Chromium) PLUS a snapshot of the partition directory's
// files (full fidelity: localStorage, IndexedDB, everything — issue #23).
// Hydrate prefers the directory snapshot, but ONLY when this process hasn't
// touched the partition yet (writing under a live session is undefined);
// within a run the live directory IS current, and the cookie path covers
// old v1 blobs and foreign-version Chromiums. The envelope is opaque bytes
// to every other layer (the store, the wire, the chain routing).
import * as fs from 'node:fs';
import * as path from 'node:path';
import { app, session as electronSession } from 'electron';

import { partitionFor } from './viewutil';

// SessionBlob is the persisted envelope. A v1 blob was a bare cookie array;
// readBlob accepts both.
interface SessionBlob {
  v: 2;
  cookies: Electron.Cookie[];
  // files maps partition-relative paths to base64 file bytes.
  files: Record<string, string>;
}

// partitionDir computes where Chromium keeps a persist: partition's files —
// needed on the hydrate side BEFORE the session exists (ses.storagePath is
// only readable on a live session, which hydrate must not create first).
function partitionDir(partition: string): string {
  const name = partition.replace(/^persist:/, '');
  return path.join(app.getPath('userData'), 'Partitions', name);
}

// snapshotDir reads every file under dir into a rel-path → base64 map.
// Returns {} when the directory doesn't exist (a never-persisted partition).
export function snapshotDir(dir: string): Record<string, string> {
  const out: Record<string, string> = {};
  const walk = (rel: string) => {
    const abs = path.join(dir, rel);
    let entries: fs.Dirent[];
    try {
      entries = fs.readdirSync(abs, { withFileTypes: true });
    } catch {
      return;
    }
    for (const e of entries) {
      const childRel = rel === '' ? e.name : `${rel}/${e.name}`;
      if (e.isDirectory()) walk(childRel);
      else if (e.isFile()) {
        try {
          out[childRel] = fs.readFileSync(path.join(dir, childRel)).toString('base64');
        } catch {
          // A file Chromium holds exclusively this instant — skip; the
          // cookie half of the blob still carries the logins.
        }
      }
    }
  };
  walk('');
  return out;
}

// restoreDir writes a snapshot back under dir (created as needed).
export function restoreDir(dir: string, files: Record<string, string>): void {
  for (const [rel, b64] of Object.entries(files)) {
    if (rel.includes('..')) continue; // path traversal guard on foreign blobs
    const abs = path.join(dir, rel);
    fs.mkdirSync(path.dirname(abs), { recursive: true });
    fs.writeFileSync(abs, Buffer.from(b64, 'base64'));
  }
}

// readBlob parses a stored blob, accepting the v2 envelope and the legacy v1
// bare cookie array. Returns null for unparseable bytes.
export function readBlob(buf: Buffer): { cookies: Electron.Cookie[]; files: Record<string, string> } | null {
  let parsed: unknown;
  try {
    parsed = JSON.parse(buf.toString('utf8'));
  } catch {
    return null;
  }
  if (Array.isArray(parsed)) return { cookies: parsed as Electron.Cookie[], files: {} };
  const env = parsed as Partial<SessionBlob>;
  if (env && env.v === 2) return { cookies: env.cookies ?? [], files: env.files ?? {} };
  return null;
}

// dehydratePartition captures the plugin's cookies and writes them back to the
// plugin's DB through the sidecar (the system of record). Best-effort, but the
// failure is reported via onError rather than swallowed (issue #46 point 4):
// silently losing a save means the user's logins quietly don't persist, with
// no signal until the NEXT session mysteriously opens logged out.
//
// flushStorageData/cookies.get/building the blob USED to sit outside this
// try — a throw there (e.g. the partition's session already torn down) was an
// unhandled promise rejection at this function's fire-and-forget call site in
// webviews.ts remove(), rather than a reported, recoverable failure. Moving
// them inside closes that gap.
export async function dehydratePartition(
  origin: string,
  pluginUuid: string,
  onError?: (message: string) => void,
): Promise<void> {
  if (!pluginUuid) return;
  const partition = partitionFor(pluginUuid);
  const ses = electronSession.fromPartition(partition);
  try {
    await ses.flushStorageData();
    const cookies = await ses.cookies.get({});
    // Full-fidelity half: the partition directory (localStorage, IndexedDB…).
    // ses.storagePath is authoritative for a live session; fall back to the
    // computed location.
    const files = snapshotDir(ses.storagePath ?? partitionDir(partition));
    const envelope: SessionBlob = { v: 2, cookies, files };
    const blob = Buffer.from(JSON.stringify(envelope), 'utf8');
    await fetch(`${origin}/session/${pluginUuid}`, { method: 'PUT', body: blob });
  } catch {
    // Sidecar gone / network blip / flush failure — the on-disk partition is
    // still the working copy, but the plugin DB (system of record) didn't get
    // this run's cookies.
    onError?.('session save failed — logins may not persist');
  }
}

// hydratePartition pulls the plugin's session blob down from its DB and sets the
// cookies into the partition, so url tiles open already logged in. Best-effort,
// but every genuine failure path now reports via onError (issue #46 point 4):
// previously a fetch/parse failure just returned, and the tile opened logged
// out with nothing telling the user why. An empty blob (buf.length === 0) is
// NOT a failure — it's the normal "never persisted a session for this plugin
// yet" case — so it stays silent.
export async function hydratePartition(
  origin: string,
  pluginUuid: string,
  onError?: (message: string) => void,
): Promise<void> {
  if (!pluginUuid) return;
  let buf: Buffer;
  try {
    const resp = await fetch(`${origin}/session/${pluginUuid}`);
    if (!resp.ok) {
      onError?.('session restore failed — page opened logged out');
      return;
    }
    buf = Buffer.from(await resp.arrayBuffer());
  } catch {
    onError?.('session restore failed — page opened logged out');
    return;
  }
  if (buf.length === 0) return;
  const parsed = readBlob(buf);
  if (parsed === null) {
    onError?.('session restore failed — page opened logged out');
    return;
  }
  const { cookies, files } = parsed;
  const partition = partitionFor(pluginUuid);
  // Directory snapshot first — but ONLY onto a partition this process hasn't
  // initialized (no on-disk dir yet): writing under a live session is
  // undefined, and within a run the live directory is already current.
  const dir = partitionDir(partition);
  if (Object.keys(files).length > 0 && !fs.existsSync(dir)) {
    try {
      restoreDir(dir, files);
    } catch {
      onError?.('session restore failed — page opened logged out');
      // Cookies below still inject; partial restore beats none.
    }
  }
  const ses = electronSession.fromPartition(partition);
  for (const c of cookies) {
    try {
      await ses.cookies.set({
        url: cookieUrl(c),
        name: c.name,
        value: c.value,
        domain: c.domain,
        path: c.path,
        secure: c.secure,
        httpOnly: c.httpOnly,
        expirationDate: c.expirationDate,
        sameSite: c.sameSite,
      });
    } catch {
      // Skip a cookie Chromium rejects (e.g. a malformed domain); the rest
      // still restore. A single bad cookie isn't "the restore failed" — it's
      // the mirror image of the empty-blob case, not worth a notice.
    }
  }
}

// cookieUrl reconstructs the origin a stored cookie belongs to, which
// cookies.set requires (cookies.get omits it).
function cookieUrl(c: Electron.Cookie): string {
  const host = (c.domain ?? '').replace(/^\./, '');
  const scheme = c.secure ? 'https' : 'http';
  return `${scheme}://${host}${c.path ?? '/'}`;
}
