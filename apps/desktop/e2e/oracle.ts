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

// tileAt returns the tile of the given kind at cell (x, y), or undefined.
// proto-JSON encodes int64 fields as either numbers or strings, so coordinates
// are compared numerically.
export function tileAt(snap: GridSnapshot, kind: string, x: number, y: number): Tile | undefined {
  return (snap.tiles ?? []).find(
    (t) => t.kind === kind && Number(t.x ?? 0) === x && Number(t.y ?? 0) === y,
  );
}
