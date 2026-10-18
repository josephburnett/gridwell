// A Connect-RPC (proto-JSON) client the e2e tests use to read the server's grid
// state independently of the renderer, because the canvas cannot say whether a
// missing tile was never created or was created and not drawn. The endpoint is
// the same loopback origin the window is served from.

const SERVICE = 'gridwell.v1.Gridwell';

export interface Tile {
  id: string;
  kind: string;
  x?: number | string;
  y?: number | string;
  w?: number | string;
  h?: number | string;
  childGridId?: string;
  linkTargetId?: string;
  link?: boolean;
  [k: string]: unknown;
}

export interface GridSnapshot {
  grid?: { id: string; version?: number | string; selection?: string[] };
  tiles?: Tile[];
}

// The fixtures register each node's auth token by origin, since two nodes in
// one test have two passwords, and every RPC carries the right one.
const authTokens = new Map<string, string>();
export function setOracleAuth(origin: string, token: string): void {
  authTokens.set(origin, token);
}
function authHeaders(origin: string): Record<string, string> {
  const token = authTokens.get(origin);
  return token ? { Cookie: `gridwell_auth=${token}` } : {};
}

// Throws on a non-OK Connect response, whose body carries the error JSON.
export async function getGrid(origin: string, gridId: string): Promise<GridSnapshot> {
  const res = await fetch(`${origin}/${SERVICE}/GetGrid`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Connect-Protocol-Version': '1',
      ...authHeaders(origin),
    },
    body: JSON.stringify({ gridId }),
  });
  if (!res.ok) {
    throw new Error(`GetGrid(${gridId}) failed: ${res.status} ${await res.text()}`);
  }
  return (await res.json()) as GridSnapshot;
}

// ── Connect streaming envelope ─────────────────────────────────────────────
// One flag byte plus a 4-byte big-endian length per message, with flag bit 0x02
// marking the trailing EndStreamResponse.

export function envelope(flags: number, payload: Buffer): Buffer {
  const head = Buffer.alloc(5);
  head.writeUInt8(flags, 0);
  head.writeUInt32BE(payload.length, 1);
  return Buffer.concat([head, payload]);
}

function* deEnvelope(body: Buffer): Generator<{ flags: number; payload: Buffer }> {
  let off = 0;
  while (off + 5 <= body.length) {
    const flags = body.readUInt8(off);
    const len = body.readUInt32BE(off + 1);
    yield { flags, payload: body.subarray(off + 5, off + 5 + len) };
    off += 5 + len;
  }
}

// ReadContent is the one content read. '' when the tile has no body; a leaf
// link resolves to its target server-side.
export async function getTileContent(origin: string, tileId: string): Promise<string> {
  const res = await fetch(`${origin}/${SERVICE}/ReadContent`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/connect+json', 'Connect-Protocol-Version': '1', ...authHeaders(origin) },
    body: new Uint8Array(envelope(0, Buffer.from(JSON.stringify({ tileId })))),
  });
  if (!res.ok) throw new Error(`ReadContent(${tileId}) failed: ${res.status} ${await res.text()}`);
  const raw = Buffer.from(await res.arrayBuffer());
  let out = '';
  for (const { flags, payload } of deEnvelope(raw)) {
    if (flags & 0x02) {
      const end = JSON.parse(payload.toString() || '{}') as { error?: unknown };
      if (end.error) throw new Error(`ReadContent(${tileId}) errored: ${JSON.stringify(end.error)}`);
      break;
    }
    const msg = JSON.parse(payload.toString()) as { data?: string };
    if (msg.data) out += Buffer.from(msg.data, 'base64').toString('utf8');
  }
  return out;
}

// Writes content straight through the server, so to the app under test this is
// a foreign writer, another device editing the same tile. version is the
// optimistic-concurrency claim.
export async function writeContent(
  origin: string,
  tileId: string,
  version: number,
  bytes: Buffer,
): Promise<void> {
  const res = await fetch(`${origin}/${SERVICE}/WriteContent`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/connect+json', 'Connect-Protocol-Version': '1', ...authHeaders(origin) },
    body: new Uint8Array(
      envelope(0, Buffer.from(JSON.stringify({ tileId, version, data: bytes.toString('base64') }))),
    ),
  });
  if (!res.ok) throw new Error(`WriteContent(${tileId}@${version}) failed: ${res.status} ${await res.text()}`);
  // An in-stream error such as a version conflict rides the end frame with
  // HTTP 200, so it is surfaced here.
  const raw = Buffer.from(await res.arrayBuffer());
  for (const { flags, payload } of deEnvelope(raw)) {
    if (flags & 0x02) {
      const end = JSON.parse(payload.toString() || '{}') as { error?: unknown };
      if (end.error) throw new Error(`WriteContent(${tileId}@${version}) errored: ${JSON.stringify(end.error)}`);
    }
  }
}

// The row as the node holds it now. A foreign writer reads it before it
// writes, because the API refuses a move or delete that names no claim.
export async function getTile(origin: string, tileId: string): Promise<Tile> {
  const res = await fetch(`${origin}/${SERVICE}/GetTile`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Connect-Protocol-Version': '1', ...authHeaders(origin) },
    body: JSON.stringify({ tileId }),
  });
  if (!res.ok) throw new Error(`GetTile(${tileId}) failed: ${res.status} ${await res.text()}`);
  return ((await res.json()) as { tile: Tile }).tile;
}

function placementOf(t: Tile) {
  return { gridId: t.gridId, x: Number(t.x ?? 0), y: Number(t.y ?? 0), w: Number(t.w ?? 0), h: Number(t.h ?? 0) };
}

// A foreign writer moving a tile out from under the app's stored links,
// claiming the placement it read.
export async function moveTile(
  origin: string,
  tileId: string,
  gridId: string,
  x: number,
  y: number,
  w: number,
  h: number,
): Promise<void> {
  const claim = placementOf(await getTile(origin, tileId));
  const res = await fetch(`${origin}/${SERVICE}/MoveTile`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Connect-Protocol-Version': '1', ...authHeaders(origin) },
    body: JSON.stringify({ tileId, gridId, x, y, w, h, claim }),
  });
  if (!res.ok) {
    throw new Error(`MoveTile(${tileId}) failed: ${res.status} ${await res.text()}`);
  }
}

// A foreign writer's select: it adds and removes tiles, claiming nothing, as
// another window or an agent would.
export async function setSelection(origin: string, gridId: string, add: string[], remove: string[] = []): Promise<void> {
  const res = await fetch(`${origin}/${SERVICE}/SetSelection`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Connect-Protocol-Version': '1', ...authHeaders(origin) },
    body: JSON.stringify({ gridId, add, remove }),
  });
  if (!res.ok) {
    throw new Error(`SetSelection(${gridId}) failed: ${res.status} ${await res.text()}`);
  }
}

// A foreign writer reframing a root grid: another device looking at the same
// place.
export async function setRootFraming(
  origin: string,
  rootGridId: string,
  cx: number,
  cy: number,
  zoom: number,
): Promise<void> {
  const res = await fetch(`${origin}/${SERVICE}/SetFraming`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Connect-Protocol-Version': '1', ...authHeaders(origin) },
    body: JSON.stringify({ rootGridId, cx, cy, zoom }),
  });
  if (!res.ok) {
    throw new Error(`SetFraming(${rootGridId}) failed: ${res.status} ${await res.text()}`);
  }
}

// A foreign writer setting a text tile's stored window: another device that
// framed the tile. w and h are what an older client stored beside the scroll.
export async function setTextScroll(
  origin: string,
  tileId: string,
  v: { x: number; y: number; w: number; h: number; mode: string },
): Promise<void> {
  const res = await fetch(`${origin}/${SERVICE}/SetTile`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Connect-Protocol-Version': '1', ...authHeaders(origin) },
    body: JSON.stringify({
      tileId,
      tile: { kind: 'text', textX: v.x, textY: v.y, textW: v.w, textH: v.h, textMode: v.mode },
    }),
  });
  if (!res.ok) {
    throw new Error(`SetTile(${tileId}) failed: ${res.status} ${await res.text()}`);
  }
}

// A foreign writer deleting a row out from under the app: another device, or
// this one in a pane the spec is not driving. It claims the row it read.
export async function deleteTile(origin: string, tileId: string): Promise<void> {
  const row = await getTile(origin, tileId);
  const res = await fetch(`${origin}/${SERVICE}/DeleteTile`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Connect-Protocol-Version': '1',
      ...authHeaders(origin),
    },
    body: JSON.stringify({ tileId, claimVersion: Number(row.version ?? 0), claim: placementOf(row) }),
  });
  if (!res.ok) {
    throw new Error(`DeleteTile(${tileId}) failed: ${res.status} ${await res.text()}`);
  }
}

// The node's verdict on the tmux a shell tile names, asked the way the
// client's descent probe asks it.
export async function shellTmuxAlive(origin: string, tileId: string): Promise<boolean> {
  const res = await fetch(`${origin}/${SERVICE}/ShellTmuxAlive`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Connect-Protocol-Version': '1',
      ...authHeaders(origin),
    },
    body: JSON.stringify({ tileId }),
  });
  if (!res.ok) {
    throw new Error(`ShellTmuxAlive(${tileId}) failed: ${res.status} ${await res.text()}`);
  }
  return Boolean(((await res.json()) as { alive?: boolean }).alive);
}

// A well whose child grid is a qualified id in another namespace. The node
// stores the link verbatim and cannot check the namespace exists, so this
// is also how a spec seeds a dangling link.
export async function createExitWell(
  origin: string,
  gridId: string,
  childGridId: string,
  altText: string,
  x: number,
  y: number,
): Promise<Tile> {
  const res = await fetch(`${origin}/${SERVICE}/CreateTile`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Connect-Protocol-Version': '1', ...authHeaders(origin) },
    body: JSON.stringify({ gridId, tile: { kind: 'well', x, y, w: 1, h: 1, childGridId, altText } }),
  });
  if (!res.ok) {
    throw new Error(`CreateTile(well -> ${childGridId}) failed: ${res.status} ${await res.text()}`);
  }
  return ((await res.json()) as { tile: Tile }).tile;
}

// A leaf link: one content tile shown here by its id. The node stores the
// target as given, so this is also how a spec seeds a link to a gone key.
export async function createLeafLink(
  origin: string,
  gridId: string,
  linkTargetId: string,
  altText: string,
  x: number,
  y: number,
  kind = 'text',
): Promise<Tile> {
  const res = await fetch(`${origin}/${SERVICE}/CreateTile`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Connect-Protocol-Version': '1', ...authHeaders(origin) },
    body: JSON.stringify({ gridId, tile: { kind, x, y, w: 1, h: 1, linkTargetId, altText } }),
  });
  if (!res.ok) {
    throw new Error(`CreateTile(link -> ${linkTargetId}) failed: ${res.status} ${await res.text()}`);
  }
  return ((await res.json()) as { tile: Tile }).tile;
}

// The Disable button's verb, pressed for a source whose notice the spec does
// not draw (a connection that answers carries no Disable button).
export async function disableSource(origin: string, namespace: string): Promise<void> {
  const res = await fetch(`${origin}/${SERVICE}/DisableSource`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Connect-Protocol-Version': '1', ...authHeaders(origin) },
    body: JSON.stringify({ namespace }),
  });
  if (!res.ok) throw new Error(`DisableSource(${namespace}) failed: ${res.status} ${await res.text()}`);
}

// writeContent for a text body, the foreign-writer specs' shape.
export async function updateText(
  origin: string,
  tileId: string,
  version: number,
  text: string,
): Promise<void> {
  return writeContent(origin, tileId, version, Buffer.from(text, 'utf8'));
}

// proto-JSON encodes int64 as a number or a string, so coordinates are compared
// numerically.
export function tileAt(snap: GridSnapshot, kind: string, x: number, y: number): Tile | undefined {
  return (snap.tiles ?? []).find(
    (t) => t.kind === kind && Number(t.x ?? 0) === x && Number(t.y ?? 0) === y,
  );
}
