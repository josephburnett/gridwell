import { test, expect, homePassword, loginToken } from './fixtures';
import { tileAt } from './oracle';
import { dumpViaDoor, TraceLine } from './trace';

// Ending an ephemeral visit inside a workspace while another of its panes
// shows the grid holding that workspace, so the workspace's own preview is on
// screen throughout. The delete and the layout that stops naming the visit are
// one gesture: the node must never hold the layout naming a deleted tile, or
// the preview drawn from it resolves the gone leaf, asks GetTile for it and
// puts "tile unavailable" on the strip. client/nav orders the two writes; the
// seam crossed here is the shim running that order against the real node.

const sameTile = (a: string, b: string) => a === b || a.endsWith('/' + b) || b.endsWith('/' + a);

function names(layout: string, id: string): boolean {
  let found = false;
  const walk = (v: unknown): void => {
    if (typeof v === 'string') found = found || sameTile(v, id);
    else if (v && typeof v === 'object') Object.values(v).forEach(walk);
  };
  walk(JSON.parse(layout || '{}'));
  return found;
}

async function workspaceDepth(window: any): Promise<number> {
  return (await window.evaluate(() => (window as any).__gridwellTest.workspace())).depth;
}

test('ending a visit inside a workspace never shows its preview a deleted leaf', async ({ gw, home, window }) => {
  const token = await loginToken(gw.origin, homePassword(home));
  await gw.enterPlugin('home');
  const scratchGridID = await gw.scratchGridID('home');
  const f = await gw.focused();
  const rootGrid = f.gridID;
  const wx = Math.round(f.cx);
  const wy = Math.round(f.cy);

  await gw.openPalette();
  await gw.dragCreate('pane', wx, wy);
  const pt = tileAt(await gw.getGrid(rootGrid), 'pane', wx, wy)!;
  expect(pt, 'pane tile persisted').toBeTruthy();
  await gw.descendCell(wx, wy);
  await expect.poll(() => workspaceDepth(window)).toBe(1);
  await expect.poll(async () => (await gw.focused()).gridID).toBe(rootGrid);

  // Two panes on the grid holding the workspace; the visit is in the second.
  await gw.splitFocusedPaneVertical();
  await expect.poll(async () => (await gw.panes()).length).toBe(2);
  const visitor = await gw.focused();
  const watcher = (await gw.panes()).find((p) => p.id !== visitor.id)!;
  expect(watcher.gridID, 'the other pane shows the grid holding the workspace').toBe(rootGrid);

  await gw.clickPaletteSwatch('url');
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?workspace-preview-inside=1`);
  await window.locator('#gw-url-form').evaluate((fm: HTMLFormElement) => fm.requestSubmit());
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  const eph = (await gw.focused()).textFocus;
  await expect
    .poll(async () => names(await gw.getTileContent(pt.id), eph), {
      message: 'the layout records the visit',
      timeout: 10_000,
    })
    .toBe(true);
  await gw.waitIdle();

  const baseline = Math.max(0, ...(await dumpViaDoor(gw.origin, token)).map((l) => l.seq ?? 0));
  await gw.middleClickPane();
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 10_000 }).toBe('');
  await expect
    .poll(async () => ((await gw.getGrid(scratchGridID)).tiles ?? []).some((t: any) => sameTile(String(t.id), eph)), {
      message: 'the ephemeral is deleted',
      timeout: 10_000,
    })
    .toBe(false);
  await expect
    .poll(async () => names(await gw.getTileContent(pt.id), eph), {
      message: 'the layout is saved without the visit',
      timeout: 10_000,
    })
    .toBe(false);
  await gw.waitIdle();
  await window.waitForTimeout(1_000);

  const after: TraceLine[] = (await dumpViaDoor(gw.origin, token)).filter((l) => (l.seq ?? 0) > baseline);
  const asked = after.filter(
    (l) => l.src === 'router' && l.kind === 'rpc' && l.msg.startsWith('GetTile error') && sameTile(l.kv?.id ?? '', eph),
  );
  expect(asked, `a preview asked for the deleted leaf: ${JSON.stringify(asked)}`).toEqual([]);
  const e = await window.evaluate(() => (window as any).__gridwellTest.errors());
  expect(e.notices, 'no notice from ending the visit').toHaveLength(0);
});
