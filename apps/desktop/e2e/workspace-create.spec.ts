import { test, expect } from './fixtures';
import { tileAt, writeContent } from './oracle';

// The palette's fifth "pane" swatch persists a kind="pane" tile, and a foreign
// layout write reaches this client's preview state: a layout is the user's
// bytes, so the write claims version 0 and its echo arrives one version on.

// The layout write rides the one content verb, WriteContent.
async function setPaneLayout(origin: string, tileId: string, version: number, layout: unknown): Promise<void> {
  await writeContent(origin, tileId, version, Buffer.from(JSON.stringify(layout)));
}

// One tile's preview signature, from the focused pane's grid.
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

  // dragCreate resolves the swatch by templateKindName, so a missing palette
  // entry fails right here.
  await gw.openPalette();
  await gw.dragCreate('pane', wx, wy);
  const snap = await gw.getGrid(rootGrid);
  const pt = tileAt(snap, 'pane', wx, wy);
  expect(pt, `a pane tile should be persisted at (${wx},${wy})`).toBeTruthy();

  // Never arranged, so no layout blob, at version 0. Polled, because waitIdle
  // can sample the gap between the RPC completing and postTileMutate's
  // background fetchGrid marking itself in flight.
  await expect
    .poll(async () => sig(window, pt!.id), { timeout: 10_000 })
    .toContain('kpane');
  const sig0 = await sig(window, pt!.id);

  // Another writer arranges the workspace, claiming version 0.
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
