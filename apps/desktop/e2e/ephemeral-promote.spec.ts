import { test, expect } from './fixtures';

// Promote: an ephemeral url visit's crumb, the bar's current square, drags onto
// another pane's grid and becomes a persistent url tile there carrying the
// visit's address. The visiting pane relocates onto the new tile, so its nav
// chain reads the new place, and the ephemeral row is deleted from the scratch
// grid. The crumb itself shows the visit's live face, not a grey placeholder.
test('dragging the ephemeral visit crumb onto another pane promotes it to a real tile', async ({
  electronApp,
  window,
  gw,
}) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const scratchGridID = (await gw.plugins()).find((l) => l.kind === 'home')!.scratchGridID;

  // Two panes on the home grid; the visit happens in the second, focused one.
  await gw.splitFocusedPaneVertical();
  const panes = await gw.panes();
  expect(panes.length, 'split into two panes').toBe(2);
  const visitor = await gw.focused();
  const dest = panes.find((p) => p.id !== visitor.id)!;

  const wcBefore = await electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length);
  await gw.clickPaletteSwatch('url');
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', 'https://example.com/promote-me');
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), { timeout: 15_000 })
    .toBeGreaterThan(wcBefore);
  const visiting = await gw.focused();
  const ephemeralID = visiting.textFocus;
  expect(ephemeralID, 'the visit is a descent').not.toBe('');

  // The current crumb (last chain segment) is the drag handle.
  const bar = await gw.bar(visiting.id);
  const crumb = bar.segments[bar.segments.length - 1];
  const from = { x: crumb.x + crumb.w / 2, y: bar.top + bar.height / 2 }; // segment x is absolute
  const to = await gw.cellCenter(dest.id, Math.round(dest.cx), Math.round(dest.cy));
  const m = window.mouse;
  await m.move(from.x, from.y);
  await m.down();
  await m.move(from.x + 6, from.y + 6);
  await m.move(to.x, to.y, { steps: 12 });
  await m.up();
  await gw.waitIdle();

  // A persistent url tile with the visit's address now lives on the home grid.
  await expect
    .poll(async () => {
      const g = await gw.getGrid(home.gridID);
      return (g.tiles ?? []).filter((t) => t.kind === 'url' && String(t.urlString ?? '').includes('promote-me')).length;
    }, { timeout: 15_000 })
    .toBe(1);
  const promoted = (await gw.getGrid(home.gridID)).tiles!.find((t) => String(t.urlString ?? '').includes('promote-me'))!;

  // The visiting pane followed its content: it is descended into the new tile on
  // the destination's grid, and the ephemeral row is gone.
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 10_000 }).toBe(promoted.id);
  expect((await gw.focused()).gridID, 'the pane relocated to where the tile lives').toBe(home.gridID);
  await expect
    .poll(async () => ((await gw.getGrid(scratchGridID)).tiles ?? []).filter((t) => t.kind === 'url').length, {
      timeout: 10_000,
    })
    .toBe(0);
});
