// Per-plugin session sync. The plugin is the session boundary: each plugin's
// url tiles render in a durable partition (persist:plugin-<uuid>), and the
// session of record lives in the plugin's DB, moved over the Gridwell interface
// via GET/PUT /session/<uuid> on the sidecar.
//
// On entering a plugin's space the host hydrates the partition from the DB blob
// (pull the session down); on flush/ascent it dehydrates back (capture). This
// is cookie-level: cookies carry "stay logged in", are portable, and move
// through the public session API with no partition-directory surgery.
// localStorage / IndexedDB are not yet synced (a documented limitation —
// snapshotting the partition directory would cover them, at a portability cost).
import { session as electronSession } from 'electron';

import { partitionFor } from './viewutil';

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
  const ses = electronSession.fromPartition(partitionFor(pluginUuid));
  try {
    await ses.flushStorageData();
    const cookies = await ses.cookies.get({});
    const blob = Buffer.from(JSON.stringify(cookies), 'utf8');
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
  let cookies: Electron.Cookie[];
  try {
    cookies = JSON.parse(buf.toString('utf8')) as Electron.Cookie[];
  } catch {
    onError?.('session restore failed — page opened logged out');
    return;
  }
  const ses = electronSession.fromPartition(partitionFor(pluginUuid));
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
