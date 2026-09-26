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
  reference?: boolean;
  [k: string]: unknown;
}

export interface GridSnapshot {
  grid?: { id: string; version?: number | string };
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

// A foreign writer moving a tile out from under the app's stored references.
export async function placeTile(
  origin: string,
  tileId: string,
  version: number | string | undefined,
  gridId: string,
  x: number,
  y: number,
  w: number,
  h: number,
): Promise<void> {
  const res = await fetch(`${origin}/${SERVICE}/PlaceTile`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Connect-Protocol-Version': '1', ...authHeaders(origin) },
    body: JSON.stringify({ tileId, version: Number(version ?? 0), gridId, x, y, w, h }),
  });
  if (!res.ok) {
    throw new Error(`PlaceTile(${tileId}) failed: ${res.status} ${await res.text()}`);
  }
}

// A foreign writer deleting a row out from under the app: another device, or
// this one in a pane the spec is not driving.
export async function deleteTile(origin: string, tileId: string): Promise<void> {
  const res = await fetch(`${origin}/${SERVICE}/DeleteTile`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Connect-Protocol-Version': '1',
      ...authHeaders(origin),
    },
    body: JSON.stringify({ tileId }),
  });
  if (!res.ok) {
    throw new Error(`DeleteTile(${tileId}) failed: ${res.status} ${await res.text()}`);
  }
}

// A well whose child grid is a qualified id in another namespace. The node
// stores the reference verbatim and cannot check the namespace exists, so this
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
