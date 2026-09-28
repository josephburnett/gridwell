import { test, expect } from './fixtures';

// A live surface refreshes the tile's shared face only while another pane
// shows it: pane.Mirrored decides, and the url pump captures exactly that set.
// A lone live pane costs no capture at all, and a second pane showing the
// tile's grid tracks the page.

// The canvas pixel at a screen point, as [r, g, b].
async function pixelAt(window: any, x: number, y: number): Promise<number[]> {
  return window.evaluate(
    ([px, py]: [number, number]) => {
      const c = document.getElementById('canvas') as HTMLCanvasElement;
      const dpr = c.width / c.getBoundingClientRect().width;
      const d = c.getContext('2d')!.getImageData(Math.round(px * dpr), Math.round(py * dpr), 1, 1).data;
      return [d[0], d[1], d[2]];
    },
    [x, y],
  );
}

test('a live url is captured only while another pane shows it', async ({ electronApp, gw, window }) => {
  const mirrorCalls = () =>
    electronApp.evaluate(() => (globalThis as any).__gwRegistry.mirrorCalls as number);
  const liveIn = (paneId: string) =>
    electronApp.evaluate((_e, id) => (globalThis as any).__gwRegistry.has(id) as boolean, paneId);
  // Paints the live page one flat color, so the face another pane draws of it
  // can be read back as a pixel.
  const paint = (paneId: string, color: string) =>
    electronApp.evaluate(
      (_e, [id, c]) =>
        (globalThis as any).__gwRegistry
          .webContentsFor(id)
          .executeJavaScript(
            `document.documentElement.style.background='${c}';document.body.style.background='${c}';document.body.innerHTML=''`,
          ),
      [paneId, color] as [string, string],
    );
  const c = await gw.cadences();

  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);
  await gw.openPalette();
  await gw.dragCreate('url', cx, cy);
  await gw.descendCell(cx, cy);
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?mirror=1`);
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await expect.poll(() => liveIn(home.id), { timeout: 15_000 }).toBe(true);
  await gw.waitIdle();

  // Alone: nothing else draws the tile, so nothing is captured.
  const alone = await mirrorCalls();
  await window.waitForTimeout(c.shellMirrorMs * 8);
  expect(await mirrorCalls(), 'a lone live pane was captured').toBe(alone);

  // Back out, then open the tile live in a new pane below. The original pane
  // stays on the grid, where the tile's face is drawn.
  await gw.middleClickPane();
  await expect.poll(() => liveIn(home.id)).toBe(false);
  await gw.ctrlDescendCell(cx, cy);
  await expect.poll(async () => (await gw.panes()).length).toBe(2);
  const below = await gw.focused();
  expect(below.id).not.toBe(home.id);
  await expect.poll(() => liveIn(below.id), { timeout: 15_000 }).toBe(true);
  const shown = await mirrorCalls();
  await expect.poll(mirrorCalls, { message: 'the mirrored pane is being captured' }).toBeGreaterThan(shown + 2);

  // The face in the grid pane follows the page.
  const face = await gw.cellCenter(home.id, cx, cy);
  await paint(below.id, '#ff0000');
  await expect
    .poll(async () => {
      const [r, g, b] = await pixelAt(window, face.x, face.y);
      return r > 200 && g < 60 && b < 60;
    }, { message: 'the grid pane shows the page gone red', timeout: 10_000 })
    .toBe(true);
  await paint(below.id, '#0000ff');
  await expect
    .poll(async () => {
      const [r, g, b] = await pixelAt(window, face.x, face.y);
      return r < 60 && g < 60 && b > 200;
    }, { message: 'and then blue', timeout: 10_000 })
    .toBe(true);

  // Leaving the live pane empties the set, and the pump stops.
  await gw.middleClickPane();
  await expect.poll(() => liveIn(below.id)).toBe(false);
  await gw.waitIdle();
  const after = await mirrorCalls();
  await window.waitForTimeout(c.shellMirrorMs * 8);
  expect(await mirrorCalls(), 'the pump ran on with nothing live').toBe(after);
});
