// The server oracle: a tiny Connect-RPC (proto-JSON) client used by the e2e
// tests to read the REAL server's grid state, independently of the renderer.
//
// This is the load-bearing idea of the harness. The canvas is opaque — a tile
// that "disappeared" might never have been created, or might have been created
// and simply not drawn. Querying the server's GetGrid splits those cases:
//   - tile present here but not on the canvas  → a render / cache / Subscribe bug
//   - tile absent here                          → a create-request routing bug
// so a single assertion against this oracle turns "nothing happened" into a
// precise, located failure.
//
// The endpoint is the same loopback origin the window is served from; the
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

// getGrid fetches a grid's tiles from the server. Throws on a non-OK Connect
// response (the body carries the Connect error JSON).
export async function getGrid(origin: string, gridId: string): Promise<GridSnapshot> {
  const res = await fetch(`${origin}/${SERVICE}/GetGrid`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Connect-Protocol-Version': '1',
    },
    body: JSON.stringify({ gridId }),
  });
  if (!res.ok) {
    throw new Error(`GetGrid(${gridId}) failed: ${res.status} ${await res.text()}`);
  }
  return (await res.json()) as GridSnapshot;
}

// ── Connect streaming envelope (2026-07-26: content rides ReadContent /
// WriteContent, Connect's enveloped streams — 1 flag byte + 4-byte BE length
// per message; flag bit 0x02 marks the trailing EndStreamResponse). ──

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
    headers: { 'Content-Type': 'application/connect+json', 'Connect-Protocol-Version': '1' },
    body: envelope(0, Buffer.from(JSON.stringify({ tileId }))),
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

// updateText writes a text tile's body DIRECTLY through the server — a
// foreign writer, as far as the app under test is concerned (another device
// editing the same tile). version is the optimistic-concurrency claim; throws
// on any non-OK response including a version conflict.
export async function updateText(
  origin: string,
  tileId: string,
  version: number,
  text: string,
): Promise<void> {
  const res = await fetch(`${origin}/${SERVICE}/WriteContent`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/connect+json', 'Connect-Protocol-Version': '1' },
    body: envelope(
      0,
      Buffer.from(
        JSON.stringify({ tileId, version, data: Buffer.from(text, 'utf8').toString('base64') }),
      ),
    ),
  });
  if (!res.ok) throw new Error(`WriteContent(${tileId}@${version}) failed: ${res.status} ${await res.text()}`);
  // Client-stream responses are enveloped too: a message frame then the
  // EndStreamResponse; an in-stream error (e.g. version conflict) rides the
  // end frame with HTTP 200, so it must be surfaced here.
  const raw = Buffer.from(await res.arrayBuffer());
  for (const { flags, payload } of deEnvelope(raw)) {
    if (flags & 0x02) {
      const end = JSON.parse(payload.toString() || '{}') as { error?: unknown };
      if (end.error) throw new Error(`WriteContent(${tileId}@${version}) errored: ${JSON.stringify(end.error)}`);
    }
  }
}

// tileAt returns the tile of the given kind at cell (x, y), or undefined.
// proto-JSON encodes int64 fields as either numbers or strings, so coordinates
// are compared numerically.
export function tileAt(snap: GridSnapshot, kind: string, x: number, y: number): Tile | undefined {
  return (snap.tiles ?? []).find(
    (t) => t.kind === kind && Number(t.x ?? 0) === x && Number(t.y ?? 0) === y,
  );
}
