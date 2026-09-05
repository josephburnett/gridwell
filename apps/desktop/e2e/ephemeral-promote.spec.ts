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

  // The current crumb (last chain segment) is the drag handle. The drop cell
  // is off the destination pane's center, so the assertion below pins WHICH
  // pane the drop resolved in: the visiting pane frames the same home grid, at
  // its descent's zoom, and a drop resolved there would land on a different
  // cell.
  const dropX = Math.round(dest.cx) + 2;
  const dropY = Math.round(dest.cy) + 1;
  const bar = await gw.bar();
  const crumb = bar.segments[bar.segments.length - 1];
  const from = { x: crumb.x + crumb.w / 2, y: bar.top + bar.height / 2 }; // segment x is absolute
  const to = await gw.cellCenter(dest.id, dropX, dropY);
  // The whole gesture in one synchronous turn. Arming the drag parks every
  // live view, which changes what sits under the REAL cursor — and Playwright
  // drives a virtual mouse, so the real one is still parked at the screen
  // center. Chromium then reports that layer change as a mousemove with no
  // button held, which is a lost release (recoverLostRelease) and ends the
  // drag wherever the real cursor happens to be. Dispatching press, move, and
  // release together leaves no window for it.
  await window.evaluate(
    ([fx, fy, tx, ty]: number[]) => {
      const canvas = document.querySelector('canvas')!;
      const fire = (type: string, x: number, y: number, buttons: number) =>
        canvas.dispatchEvent(
          new MouseEvent(type, { clientX: x, clientY: y, buttons, button: 0, bubbles: true }),
        );
      fire('mousedown', fx, fy, 1);
      fire('mousemove', tx, ty, 1);
      fire('mouseup', tx, ty, 0);
    },
    [from.x, from.y, to.x, to.y],
  );
  await gw.waitIdle();

  // A persistent url tile with the visit's address now lives on the home grid.
  await expect
    .poll(async () => {
      const g = await gw.getGrid(home.gridID);
      return (g.tiles ?? []).filter((t) => t.kind === 'url' && String(t.urlString ?? '').includes('promote-me')).length;
    }, { timeout: 15_000 })
    .toBe(1);
  const promoted = (await gw.getGrid(home.gridID)).tiles!.find((t) => String(t.urlString ?? '').includes('promote-me'))!;
  expect(
    [Number(promoted.x ?? 0), Number(promoted.y ?? 0)],
    'the tile landed on the cell of the pane the drop resolved in',
  ).toEqual([dropX, dropY]);

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
