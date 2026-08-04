import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Issue #77: ascending from a shell that lives INSIDE a well (a descended
// sub-grid) must persist its frozen preview. The freeze writeback resolves the
// tile against the descent path's leaf grid; a shell in a sub-grid therefore
// needs the pane's path sent with SetShellPreview — the URL twin captures it,
// the shell path historically didn't, so the save failed with "descent path is
// invalid: tile N not in path leaf grid 1" and surfaced as an error notice.
// This spec crosses the whole seam: create-in-subgrid → live PTY → ascend →
// preview persisted on the server, no error on the strip.

async function errors(window: any) {
  return window.evaluate(() => (window as any).__gridwellTest.errors());
}

test('ascending a shell inside a well persists its preview', async ({ gw, window, electronApp }) => {
  await gw.enterPlugin('localdb');
  const home = await gw.focused();
  const wx = Math.round(home.cx);
  const wy = Math.round(home.cy);

  // A well, and a descent into its (empty) child grid.
  await gw.openPalette();
  await gw.dragCreate('well', wx, wy);
  const well = tileAt(await gw.getGrid(home.gridID), 'well', wx, wy)!;
  expect(well, 'well created').toBeTruthy();
  const child = well.childGridId as string;
  await gw.descendCell(wx, wy);
  await expect.poll(async () => (await gw.focused()).gridID).toBe(child);

  // A shell INSIDE the well. dragCreate auto-descends and spawns the PTY.
  const inWell = await gw.focused();
  const sx = Math.round(inWell.cx);
  const sy = Math.round(inWell.cy);
  await gw.openPalette();
  await gw.dragCreate('shell', sx, sy);
  await gw.descendCell(sx, sy); // a drop lands bare (#241); the descent creates the session
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  const shell = tileAt(await gw.getGrid(child), 'shell', sx, sy)!;
  expect(shell, 'shell created in the sub-grid').toBeTruthy();
  expect(Number(shell.previewBlobId ?? 0), 'fresh shell has no preview yet').toBe(0);

  // Put visible content on the terminal so the frozen preview has glyphs to
  // show — the content assertion below depends on it.
  await window.keyboard.type('echo FREEZE-MARKER-LINE');
  await window.keyboard.press('Enter');
  await window.waitForTimeout(500);

  // Ascend from the live shell (bar crumb click). The freeze capture +
  // SetShellPreview run on this path.
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');

  // The preview must land on the server: the tile in the SUB-grid gains a
  // preview blob. Before the fix this write was rejected (invalid path).
  await expect
    .poll(async () => Number(tileAt(await gw.getGrid(child), 'shell', sx, sy)?.previewBlobId ?? 0), {
      timeout: 10_000,
    })
    .toBeGreaterThan(0);

  // And nothing surfaced on the error strip from the shell freeze.
  const e = await errors(window);
  expect(
    e.notices.filter((n: any) => n.source === 'shell'),
    'no shell error notice after ascent',
  ).toHaveLength(0);

  // The preview must show the TERMINAL, not a blank layer: a blob id alone
  // proved nothing when the capture grabbed the transparent link-layer canvas
  // (the WebGL renderer's first-in-DOM canvas — issue #84). Decode the stored
  // JPEG in the main process and require bright glyph pixels.
  const jpegB64 = await window.evaluate(async ([org, tileId]: string[]) => {
    const r = await fetch(`${org}/gridwell.v1.Gridwell/GetTilePreview`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'Connect-Protocol-Version': '1' },
      body: JSON.stringify({ tileId }),
    });
    return ((await r.json()) as { jpeg?: string }).jpeg ?? '';
  }, [gw.origin, shell.id]);
  expect(jpegB64.length, 'preview jpeg has bytes').toBeGreaterThan(0);
  const bright = await electronApp.evaluate(({ nativeImage }, b64: string) => {
    const img = nativeImage.createFromBuffer(Buffer.from(b64, 'base64'));
    const bmp = img.toBitmap(); // BGRA
    let n = 0;
    for (let i = 0; i < bmp.length; i += 4) {
      if (bmp[i + 1] > 0x90 && bmp[i + 2] > 0x90) n++;
    }
    return n;
  }, jpegB64);
  expect(bright, 'frozen preview contains rendered glyph pixels').toBeGreaterThan(50);

  // Leave clean: delete the shell tile so its tmux session is killed and
  // teardown doesn't hang on a live PTY.
  await gw.deleteTileCell(sx, sy);
});
