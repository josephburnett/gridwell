import { test, expect } from './fixtures';
import { tileAt, writeContent } from './oracle';

// The pane tile on the client: the palette carries a fifth "pane" swatch,
// drag-creating it persists a kind="pane" tile server-side, and a foreign layout
// write reaches this client's preview-relevant state. The last assertion pins
// the delivery seam. A pane layout is framing-class, so the version stays put
// and the event echo arrives at the same version, with only the blob field
// distinguishing it; a same-version event must still apply, since the interlock
// drops only strictly older events, for the preview signature to move. The stale
// content-bytes half of that echo, cache.Apply's blob-change drop, is pinned by
// unit test in client/cache.

// The layout write rides the one content door, WriteContent.
async function setPaneLayout(origin: string, tileId: string, version: number, layout: unknown): Promise<void> {
  await writeContent(origin, tileId, version, Buffer.from(JSON.stringify(layout)));
}

// sig reads one tile's preview signature from the focused pane's grid.
async function sig(window: any, tileId: string): Promise<string> {
  const sigs = await window.evaluate(
    () => (window as any).__gridwellTest.previewSigs() as Record<string, string>,
  );
  return sigs[tileId] ?? '';
}

test('workspace primitive: drag-create persists a pane tile and its preview tracks the layout blob', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const rootGrid = f.gridID;
  const wx = Math.round(f.cx);
  const wy = Math.round(f.cy);

  // The fifth swatch exists and creates: dragCreate resolves the swatch by its
  // templateKindName, so a missing palette entry fails right here.
  await gw.openPalette();
  await gw.dragCreate('pane', wx, wy);
  const snap = await gw.getGrid(rootGrid);
  const pt = tileAt(snap, 'pane', wx, wy);
  expect(pt, `a pane tile should be persisted at (${wx},${wy})`).toBeTruthy();

  // Baseline: never arranged, so no layout blob, at version 0. Polled, because
  // the oracle above proved server truth but the client cache refresh is
  // postTileMutate's background fetchGrid, and waitIdle can sample the gap
  // between the RPC completing and that goroutine marking itself in flight.
  await expect
    .poll(async () => sig(window, pt!.id), { timeout: 10_000 })
    .toContain('kpane');
  const sig0 = await sig(window, pt!.id);

  // Another writer arranges the workspace. The layout write is framing-class, so
  // the version stays 0 and only the blob-change invalidation can propagate it.
  await setPaneLayout(gw.origin, pt!.id, 0, {
    v: 1,
    root: {
      split: {
        dir: 'v', ratio: 0.4,
        a: { pane: { id: 'p1', zoom: 1 } },
        b: { pane: { id: 'p2', zoom: 1 } },
      },
    },
    focus: 'p2',
  });
  await expect
    .poll(async () => sig(window, pt!.id), {
      message: 'preview signature must pick up the new layout blob via SSE',
      timeout: 10_000,
    })
    .not.toBe(sig0);
});
