// The server oracle: a small Connect-RPC (proto-JSON) client the e2e tests use
// to read the real server's grid state, independently of the renderer.
//
// The canvas is opaque: a tile that disappeared might never have been created,
// or might have been created and not drawn. Querying the server's GetGrid
// splits those cases:
//   - present here but not on the canvas → a render, cache, or Subscribe bug
//   - absent here                        → a create-request routing bug
// so one assertion against the oracle turns "nothing happened" into a located
// failure.
//
// The endpoint is the same loopback origin the window is served from, and the
// Connect handler is mounted at /<package>.<service>/<method>.

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

// The web door is always password-gated. The fixtures register each served
// node's auth token by origin, since two nodes in one test have two passwords,
// and every RPC carries the right one as the cookie a browser would.
const authTokens = new Map<string, string>();
export function setOracleAuth(origin: string, token: string): void {
  authTokens.set(origin, token);
}
function authHeaders(origin: string): Record<string, string> {
  const token = authTokens.get(origin);
  return token ? { Cookie: `gridwell_auth=${token}` } : {};
}

// getGrid fetches a grid's tiles from the server. Throws on a non-OK Connect
// response (the body carries the Connect error JSON).
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
// Content rides ReadContent and WriteContent, which are Connect enveloped
// streams: one flag byte plus a 4-byte big-endian length per message, with flag
// bit 0x02 marking the trailing EndStreamResponse.

function envelope(flags: number, payload: Buffer): Buffer {
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

// getTileContent fetches a tile's body bytes through ReadContent (the one
// content read), reassembling the enveloped JSON chunk stream. Returns ''
// when the tile has no body. A leaf link resolves to its target server-side.
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

// writeContent writes a tile's content bytes directly through the server, so to
// the app under test it is a foreign writer: another device editing the same
// tile. This is the one content door: a text body bumps the version, while a
// pane layout is framing-class. version is the optimistic-concurrency claim.
// Throws on any non-OK response, including a version conflict riding the
// end-stream frame.
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
  // Client-stream responses are enveloped too: a message frame, then the
  // EndStreamResponse. An in-stream error such as a version conflict rides the
  // end frame with HTTP 200, so it must be surfaced here.
  const raw = Buffer.from(await res.arrayBuffer());
  for (const { flags, payload } of deEnvelope(raw)) {
    if (flags & 0x02) {
      const end = JSON.parse(payload.toString() || '{}') as { error?: unknown };
      if (end.error) throw new Error(`WriteContent(${tileId}@${version}) errored: ${JSON.stringify(end.error)}`);
    }
  }
}

// placeTile moves or resizes a tile directly through the server: a foreign
// writer moving it out from under the app's stored references, which is the
// relocation specs' shape. Unary Connect JSON, like getGrid.
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

// createExitWell creates a link well directly through the server: a well whose
// child grid is a qualified id in another namespace. The node stores the
// reference verbatim and never checks that the namespace exists — it cannot,
// since the namespace may be a remote's — so this is also how a spec seeds a
// DANGLING link, one into a namespace this node does not declare.
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

// updateText is writeContent for a text body, the foreign-writer specs' shape.
export async function updateText(
  origin: string,
  tileId: string,
  version: number,
  text: string,
): Promise<void> {
  return writeContent(origin, tileId, version, Buffer.from(text, 'utf8'));
}

// tileAt returns the tile of the given kind at cell (x, y), or undefined.
// proto-JSON encodes int64 fields as either numbers or strings, so coordinates
// are compared numerically.
export function tileAt(snap: GridSnapshot, kind: string, x: number, y: number): Tile | undefined {
  return (snap.tiles ?? []).find(
    (t) => t.kind === kind && Number(t.x ?? 0) === x && Number(t.y ?? 0) === y,
  );
}
