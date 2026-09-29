import { test, expect, homePassword, loginToken } from './fixtures';
import { tileAt } from './oracle';
import { dumpViaDoor, TraceLine } from './trace';

// A pane tile's preview draws the layout its row names now. An ephemeral url
// visit inside a workspace ends and its tile is deleted, the layout is saved
// without it, and the next look at the workspace from outside must not draw
// the arrangement from before: drawing it resolves the gone leaf, which asks
// GetTile for it and puts "tile unavailable" on the strip. The layout memo and
// the content cache are js-free (client/panepreview, client/cache); the seam
// crossed here is the shim's save glue feeding them in the real app.

const sameTile = (a: string, b: string) => a === b || a.endsWith('/' + b) || b.endsWith('/' + a);

// names reports that a stored layout's frames name the tile.
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

test("a workspace preview never draws a leaf its layout no longer names", async ({ gw, home, window }) => {
  const token = await loginToken(gw.origin, homePassword(home));
  await gw.enterPlugin('home');
  const scratchGridID = await gw.scratchGridID('home');
  const f = await gw.focused();
  const rootGrid = f.gridID;
  const wx = Math.round(f.cx);
  const wy = Math.round(f.cy);

  await gw.openPalette();
  await gw.dragCreate('pane', wx, wy);
  await gw.openPalette();
  await gw.dragCreate('well', wx + 1, wy);
  const pt = tileAt(await gw.getGrid(rootGrid), 'pane', wx, wy)!;
  expect(pt, 'pane tile persisted').toBeTruthy();
  await gw.descendCell(wx, wy);
  await expect.poll(() => workspaceDepth(window)).toBe(1);
  // Into the well, so the workspace's pane does not show the grid holding the
  // workspace itself and draws no preview of it from inside.
  await gw.descendCell(wx + 1, wy);
  await expect.poll(async () => (await gw.focused()).gridID).not.toBe(rootGrid);

  // An ephemeral url visit in the workspace's pane, recorded in its layout.
  await gw.clickPaletteSwatch('url');
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?workspace-preview=1`);
  await window.locator('#gw-url-form').evaluate((fm: HTMLFormElement) => fm.requestSubmit());
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  const eph = (await gw.focused()).textFocus;
  await expect
    .poll(async () => names(await gw.getTileContent(pt.id), eph), {
      message: 'the layout records the visit',
      timeout: 10_000,
    })
    .toBe(true);

  // Out and back: the preview drawn from outside decodes the layout with the
  // visit in it, which is what the memo and the cache then hold.
  await gw.leaveWorkspace();
  await expect.poll(() => workspaceDepth(window)).toBe(0);
  await gw.waitIdle();
  await window.waitForTimeout(1_000);
  await gw.descendCell(wx, wy);
  await expect.poll(() => workspaceDepth(window)).toBe(1);
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).toBe(eph);

  // Ending the visit deletes the ephemeral and rewrites the layout without it.
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

  // The look from outside, after the rewrite.
  await gw.leaveWorkspace();
  await expect.poll(() => workspaceDepth(window)).toBe(0);
  await gw.waitIdle();
  await window.waitForTimeout(2_000);

  const after: TraceLine[] = (await dumpViaDoor(gw.origin, token)).filter((l) => (l.seq ?? 0) > baseline);
  // The ascent itself reads the ephemeral before deleting it; any read after
  // the delete is refused, so a refusal is the count.
  const asked = after.filter(
    (l) => l.src === 'router' && l.kind === 'rpc' && l.msg.startsWith('GetTile error') && sameTile(l.kv?.id ?? '', eph),
  );
  expect(asked, `a preview asked for the deleted leaf: ${JSON.stringify(asked)}`).toEqual([]);
  const e = await window.evaluate(() => (window as any).__gridwellTest.errors());
  expect(e.notices, 'no notice from looking at the workspace').toHaveLength(0);
});
