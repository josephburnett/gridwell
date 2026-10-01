import { test, expect, homePassword, loginToken } from './fixtures';
import { tileAt } from './oracle';
import { dumpViaDoor, TraceLine } from './trace';
import type { GridwellDriver } from './driver';

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

// A workspace on the home grid, split in two, an ephemeral url visit open in
// the focused pane and recorded in the layout. The other pane shows the grid
// holding the workspace.
async function visitInsideWorkspace(gw: GridwellDriver, window: any) {
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
  return { pt, eph, scratchGridID };
}

test('ending a visit inside a workspace never shows its preview a deleted leaf', async ({ gw, home, window }) => {
  const token = await loginToken(gw.origin, homePassword(home));
  const { pt, eph, scratchGridID } = await visitInsideWorkspace(gw, window);

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

// A debounced layout save still on the wire when the visit ends must land
// before the flush that stops naming the visit, or the node keeps a layout
// naming the tile the retire deleted. The first layout write is held at the
// network until the ascent has had time to flush and retire; outbox.SaveQueue
// is what keeps the flush behind it.
test('a layout save in flight when a visit ends never lands over the flush', async ({ gw, window }) => {
  const { pt, eph, scratchGridID } = await visitInsideWorkspace(gw, window);
  const ptID = String(pt.id).split('/').pop()!;

  let release: (() => Promise<void>) | null = null;
  await window.route('**/gridwell.v1.Gridwell/WriteContent', async (r: any) => {
    if (!release && (r.request().postData() ?? '').includes(ptID)) {
      release = () => r.continue();
      return;
    }
    await r.continue();
  });

  // A divider drag changes the layout and keeps the visit; its debounced
  // save is the write held. Raw input: the driver's gestures wait for idle,
  // which a held write never reaches.
  try {
    // The press lands on the watcher's side of the border, the visitor's
    // being its live page.
    const visitor = (await gw.panes()).find((p) => p.textFocus === eph)!;
    const watcher = (await gw.panes()).find((p) => p.id !== visitor.id)!;
    const right = watcher.x > visitor.x;
    const bx = right ? watcher.x + 2 : watcher.x + watcher.w - 2;
    const by = watcher.y + watcher.h / 2;
    const step = right ? 6 : -6;
    await window.mouse.move(bx, by);
    await window.mouse.down({ button: 'left' });
    await window.mouse.move(bx + step, by);
    await window.mouse.move(bx + 10 * step, by, { steps: 10 });
    await window.mouse.up({ button: 'left' });
    await expect
      .poll(() => release !== null, { message: 'the debounced layout save is on the wire', timeout: 10_000 })
      .toBe(true);

    const v = (await gw.panes()).find((p) => p.id === visitor.id)!;
    if ((await gw.focused()).id !== v.id) await window.mouse.click(v.x + v.w / 2, v.y + v.h / 2);
    await expect.poll(async () => (await gw.focused()).textFocus, { message: 'the visit is open and focused' }).toBe(eph);
    await window.mouse.click(v.x + v.w / 2, v.y + v.h / 2, { button: 'middle' });
    await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 10_000 }).toBe('');
    // Time for an unordered flush to land and retire the visit.
    await window.waitForTimeout(1_500);
  } finally {
    if (release) await (release as () => Promise<void>)();
    await window.unroute('**/gridwell.v1.Gridwell/WriteContent');
  }
  await gw.waitIdle();

  await expect
    .poll(async () => ((await gw.getGrid(scratchGridID)).tiles ?? []).some((t: any) => sameTile(String(t.id), eph)), {
      message: 'the ephemeral is deleted',
      timeout: 10_000,
    })
    .toBe(false);
  await window.waitForTimeout(1_000);
  expect(names(await gw.getTileContent(pt.id), eph), 'the node holds a layout naming the deleted visit').toBe(false);
});
