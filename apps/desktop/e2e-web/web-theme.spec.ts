import { test, expect } from './fixtures';

// A phone has no native menu, so the circle's right-click draws its own DOM
// popover. Same owner (client/circlemenu), same items, same preference: this
// suite is the other renderer's half of e2e/theme.spec.ts.

async function groundPixel(window: any, x: number, y: number): Promise<number> {
  return window.evaluate(
    ([px, py]: [number, number]) => {
      const c = document.getElementById('canvas') as HTMLCanvasElement;
      const ctx = c.getContext('2d')!;
      const dpr = c.width / c.getBoundingClientRect().width;
      const d = ctx.getImageData(Math.round(px * dpr), Math.round(py * dpr), 1, 1).data;
      return (d[0] + d[1] + d[2]) / 3;
    },
    [x, y],
  );
}

test('the circle popover switches the theme and the browser remembers it', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('home');
  expect(await gw.theme(), 'a browser with nothing stored is dark').toBe('dark');

  const f = await gw.focused();
  const px = f.x + f.w / 2;
  const py = f.y + 12;
  expect(await groundPixel(window, px, py)).toBeLessThan(80);

  await gw.rightClickCircle();
  const popover = window.locator('#gw-circle-menu');
  await popover.waitFor({ timeout: 5_000 });
  await expect(popover.locator('[data-gw-choice]')).toHaveCount(3); // both themes and the dump
  // The body's own background rides the same --gw- properties the canvas
  // reads, so the page chrome cannot stay dark behind a light grid.
  const darkBody = await window.evaluate(() =>
    getComputedStyle(document.documentElement).getPropertyValue('--gw-bg').trim(),
  );

  await popover.locator('[data-gw-choice="light"]').click();
  await expect(popover).toHaveCount(0);
  await expect.poll(() => gw.theme(), { timeout: 10_000 }).toBe('light');
  await expect.poll(() => groundPixel(window, px, py), { timeout: 10_000 }).toBeGreaterThan(180);
  const lightBody = await window.evaluate(() =>
    getComputedStyle(document.documentElement).getPropertyValue('--gw-bg').trim(),
  );
  expect(lightBody, 'the DOM custom property follows the canvas').not.toBe(darkBody);

  // localStorage, not the node: a reload of the same browser comes back light.
  await window.reload();
  await window.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
  await gw.waitIdle();
  await expect.poll(() => gw.theme(), { timeout: 20_000 }).toBe('light');

  // Dismissal changes nothing: Escape closes the popover with the theme as it
  // was, so the gesture is a choice and never a toggle.
  await gw.rightClickCircle();
  await popover.waitFor({ timeout: 5_000 });
  await window.keyboard.press('Escape');
  await expect(popover).toHaveCount(0);
  expect(await gw.theme()).toBe('light');

  await gw.rightClickCircle();
  await popover.waitFor({ timeout: 5_000 });
  await popover.locator('[data-gw-choice="dark"]').click();
  await expect.poll(() => gw.theme(), { timeout: 10_000 }).toBe('dark');
  await expect.poll(() => groundPixel(window, px, py), { timeout: 10_000 }).toBeLessThan(80);
});
