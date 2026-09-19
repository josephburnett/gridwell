import { test, expect } from './fixtures';

// Dark or light is a client view preference: nothing about it reaches the
// store or the wire, so the seam runs from the canvas right-click on the bar
// circle, through barslot/circlemenu, out to the host's native menu, and back
// into client/theme. The oracle is the hook plus a canvas pixel, because the
// palette's only other evidence is pixels.

// The grid's background pixel, a little inside the focused pane, as
// "#rrggbb". A grid draws a.pal.Bg there whatever else is on it.
async function groundPixel(window: any, x: number, y: number): Promise<string> {
  return window.evaluate(
    ([px, py]: [number, number]) => {
      const c = document.getElementById('canvas') as HTMLCanvasElement;
      const ctx = c.getContext('2d')!;
      const dpr = c.width / c.getBoundingClientRect().width;
      const d = ctx.getImageData(Math.round(px * dpr), Math.round(py * dpr), 1, 1).data;
      const hex = (n: number) => n.toString(16).padStart(2, '0');
      return `#${hex(d[0])}${hex(d[1])}${hex(d[2])}`;
    },
    [x, y],
  );
}

// A shade's average channel. The two palettes are built around near-black and
// near-white grounds, so this separates them without pinning a value that is
// the designer's to change.
function brightness(hex: string): number {
  const n = parseInt(hex.slice(1), 16);
  return ((n >> 16) + ((n >> 8) & 0xff) + (n & 0xff)) / 3;
}

// Picks a row out of the intercepted native menu and clicks it.
async function chooseFromNativeMenu(electronApp: any, label: string): Promise<void> {
  await electronApp.evaluate((_: unknown, wanted: string) => {
    const m = (globalThis as any).__gwChoiceMenu;
    if (!m) throw new Error('no menu popped');
    const item = m.items.find((i: any) => i.label === wanted);
    if (!item) throw new Error(`no ${wanted} row; got ${m.items.map((i: any) => i.label).join(', ')}`);
    item.click();
  }, label);
}

test('the bar circle right-clicks to a theme menu; light survives a reload', async ({
  electronApp,
  gw,
  window,
}) => {
  await gw.enterPlugin('home');
  expect(await gw.theme(), 'a client with no stored preference is dark').toBe('dark');

  const f = await gw.focused();
  // A point inside the pane and well clear of the bar band, so the pixel read
  // is the grid's ground.
  const px = f.x + f.w / 2;
  const py = f.y + 12;
  const darkGround = await groundPixel(window, px, py);
  expect(brightness(darkGround), `dark ground ${darkGround} must be near black`).toBeLessThan(80);

  // A native popup blocks under xvfb, so Menu.popup is intercepted and the
  // menu stashed; the item's own click still settles main's promise.
  await electronApp.evaluate(({ Menu }) => {
    const g = globalThis as any;
    g.__gwChoiceOrigPopup = Menu.prototype.popup;
    g.__gwChoiceMenu = null;
    (Menu.prototype as any).popup = function (this: any) {
      g.__gwChoiceMenu = this;
      return undefined;
    };
  });

  try {
    await gw.rightClickCircle();
    await expect
      .poll(() => electronApp.evaluate(() => Boolean((globalThis as any).__gwChoiceMenu)), {
        timeout: 10_000,
      })
      .toBe(true);
    const labels = await electronApp.evaluate(() =>
      (globalThis as any).__gwChoiceMenu.items.map((i: any) => i.label),
    );
    expect(labels, 'both themes are offered').toEqual(['Dark mode', 'Light mode']);
    const checked = await electronApp.evaluate(() =>
      (globalThis as any).__gwChoiceMenu.items.filter((i: any) => i.checked).map((i: any) => i.label),
    );
    expect(checked, 'the theme on screen is the checked one').toEqual(['Dark mode']);

    await chooseFromNativeMenu(electronApp, 'Light mode');
    await expect.poll(() => gw.theme(), { timeout: 10_000 }).toBe('light');
    const lightGround = await groundPixel(window, px, py);
    expect(
      brightness(lightGround),
      `light ground ${lightGround} must be near white`,
    ).toBeGreaterThan(180);

    // The preference is the browser's, not the node's: a reload comes back
    // light with nothing having been written to the server.
    await window.reload();
    await window.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
    await gw.waitIdle();
    await expect.poll(() => gw.theme(), { timeout: 20_000 }).toBe('light');
    expect(brightness(await groundPixel(window, px, py))).toBeGreaterThan(180);

    // And back. The menu now checks light, so the check follows the palette
    // rather than a remembered first answer.
    await electronApp.evaluate(() => {
      (globalThis as any).__gwChoiceMenu = null;
    });
    await gw.rightClickCircle();
    await expect
      .poll(() => electronApp.evaluate(() => Boolean((globalThis as any).__gwChoiceMenu)), {
        timeout: 10_000,
      })
      .toBe(true);
    const nowChecked = await electronApp.evaluate(() =>
      (globalThis as any).__gwChoiceMenu.items.filter((i: any) => i.checked).map((i: any) => i.label),
    );
    expect(nowChecked).toEqual(['Light mode']);
    await chooseFromNativeMenu(electronApp, 'Dark mode');
    await expect.poll(() => gw.theme(), { timeout: 10_000 }).toBe('dark');
    expect(brightness(await groundPixel(window, px, py))).toBeLessThan(80);
  } finally {
    await electronApp.evaluate(({ Menu }) => {
      const g = globalThis as any;
      if (g.__gwChoiceOrigPopup) (Menu.prototype as any).popup = g.__gwChoiceOrigPopup;
      delete g.__gwChoiceOrigPopup;
      delete g.__gwChoiceMenu;
    });
  }
});
