import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Ascending from a shell that lives inside a well, in a descended sub-grid, must
// persist its frozen preview. The freeze writeback resolves the tile against the
// descent path's leaf grid, so a shell in a sub-grid needs the pane's path sent
// with SetShellPreview; without it the save fails with "descent path is invalid"
// and surfaces as an error notice. This spec crosses the whole seam: create in a
// sub-grid, live PTY, ascend, preview persisted on the server, nothing on the
// error strip.

async function errors(window: any) {
  return window.evaluate(() => (window as any).__gridwellTest.errors());
}

test('ascending a shell inside a well persists its preview', async ({ gw, window, electronApp }) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const wx = Math.round(home.cx);
  const wy = Math.round(home.cy);

  // A well, and a descent into its empty child grid.
  await gw.openPalette();
  await gw.dragCreate('well', wx, wy);
  const well = tileAt(await gw.getGrid(home.gridID), 'well', wx, wy)!;
  expect(well, 'well created').toBeTruthy();
  const child = well.childGridId as string;
  await gw.descendCell(wx, wy);
  await expect.poll(async () => (await gw.focused()).gridID).toBe(child);

  // A shell inside the well.
  const inWell = await gw.focused();
  const sx = Math.round(inWell.cx);
  const sy = Math.round(inWell.cy);
  await gw.openPalette();
  await gw.dragCreate('shell', sx, sy);
  await gw.descendCell(sx, sy); // the drop lands bare; the descent creates the session
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  const shell = tileAt(await gw.getGrid(child), 'shell', sx, sy)!;
  expect(shell, 'shell created in the sub-grid').toBeTruthy();
  expect(Number(shell.previewBlobId ?? 0), 'fresh shell has no preview yet').toBe(0);

  // Put visible content on the terminal so the frozen preview has glyphs to
  // show; the content assertion below depends on it.
  await window.keyboard.type('echo FREEZE-MARKER-LINE');
  await window.keyboard.press('Enter');
  // Wait for echo's output line. The typed command also carries the marker, so a
  // whole-buffer match would pass early.
  await expect
    .poll(async () => {
      const t: string = await window.evaluate(() => (window as any).__gridwellTest.shellText());
      return t.split('\n').some((l) => l.includes('FREEZE-MARKER-LINE') && !l.includes('echo '));
    }, { timeout: 10_000 })
    .toBe(true);

  // Ascend from the live shell with a bar crumb click. The freeze capture and
  // SetShellPreview run on this path.
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');

  // The preview must land on the server: the tile in the sub-grid gains a preview
  // blob. Without the path the write is rejected as an invalid path.
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

  // The preview must show the terminal, not a blank layer. A blob id alone proves
  // nothing if the capture grabbed the transparent link-layer canvas, which is
  // the WebGL renderer's first canvas in the DOM. So decode the stored JPEG in
  // the main process and require bright glyph pixels.
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

  // Leave clean: delete the shell tile so its tmux session is killed and teardown
  // does not hang on a live PTY.
  await gw.deleteTileCell(sx, sy);
});
