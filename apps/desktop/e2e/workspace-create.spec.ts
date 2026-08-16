import { test, expect } from './fixtures';
import { tileAt, writeContent } from './oracle';

// The pane tile's client birth (stage 3 of the workspace primitive): the
// palette carries a fifth "pane" swatch, drag-creating it persists a
// kind="pane" tile server-side, and a foreign layout write reaches this
// client's preview-relevant state. The last assertion pins the delivery
// seam: SetPaneLayout is framing-class (version stays 0), so the SSE echo
// arrives at the SAME version and only the blob field distinguishes it —
// a same-version event must still apply (the I11 interlock drops only
// STRICTLY older events) for the preview signature to move. The stale
// CONTENT-bytes half of that echo (cache.Apply's blob-change drop) is
// pinned separately by unit test in client/cache.

// The layout write rides the one content door (WriteContent — framing-class
// for pane layouts, so the version stays put and the SSE echo is
// same-version, exactly the seam this spec pins).
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
  await gw.enterPlugin('local');
  const f = await gw.focused();
  const rootGrid = f.gridID;
  const wx = Math.round(f.cx);
  const wy = Math.round(f.cy);

  // The fifth swatch exists and creates: dragCreate resolves the swatch by
  // its templateKindName, so a missing palette entry fails right here.
  await gw.openPalette();
  await gw.dragCreate('pane', wx, wy);
  const snap = await gw.getGrid(rootGrid);
  const pt = tileAt(snap, 'pane', wx, wy);
  expect(pt, `a pane tile should be persisted at (${wx},${wy})`).toBeTruthy();

  // Baseline: never-arranged (no layout blob), version 0. POLLED: the
  // oracle above proved server truth, but the client cache refresh is
  // postTileMutate's background fetchGrid — waitIdle can sample the gap
  // between the RPC completing and that goroutine marking itself
  // inflight, so a one-shot read raced it under suite load (2×, same
  // line, isolated-green — the classic shape).
  await expect
    .poll(async () => sig(window, pt!.id), { timeout: 10_000 })
    .toContain('kpane');
  const sig0 = await sig(window, pt!.id);

  // Another writer arranges the workspace. The layout write is framing-class
  // (version stays 0), so ONLY the blob-change invalidation can propagate it.
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
