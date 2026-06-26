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
// plugin's DB through the sidecar (the system of record). Best-effort.
export async function dehydratePartition(origin: string, pluginUuid: string): Promise<void> {
  if (!pluginUuid) return;
  const ses = electronSession.fromPartition(partitionFor(pluginUuid));
  await ses.flushStorageData();
  const cookies = await ses.cookies.get({});
  const blob = Buffer.from(JSON.stringify(cookies), 'utf8');
  try {
    await fetch(`${origin}/session/${pluginUuid}`, { method: 'PUT', body: blob });
  } catch {
    // Sidecar gone / network blip — the on-disk partition is still the working
    // copy; the next dehydrate catches up.
  }
}

// hydratePartition pulls the plugin's session blob down from its DB and sets the
// cookies into the partition, so url tiles open already logged in. Best-effort.
export async function hydratePartition(origin: string, pluginUuid: string): Promise<void> {
  if (!pluginUuid) return;
  let buf: Buffer;
  try {
    const resp = await fetch(`${origin}/session/${pluginUuid}`);
    if (!resp.ok) return;
    buf = Buffer.from(await resp.arrayBuffer());
  } catch {
    return;
  }
  if (buf.length === 0) return;
  let cookies: Electron.Cookie[];
  try {
    cookies = JSON.parse(buf.toString('utf8')) as Electron.Cookie[];
  } catch {
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
      // still restore.
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
