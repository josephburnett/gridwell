import { test, expect } from './fixtures';

// Raw text must not reflow when pane focus moves, so the canvas painter an
// unfocused descended pane shows must soft-wrap to the same columns the editing
// textarea does. The textarea's rendered row count must match the painter's,
// read through the rawRows hook; one row per source line diverges by dozens.

test('the canvas paints the rows the textarea soft-wraps', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  await gw.openMenu();
  await gw.dragCreate('markdown', cx, cy);
  await gw.descendCell(cx, cy);
  await expect.poll(async () => (await gw.focused()).textFocus).not.toBe('');

  // A paragraph that soft-wraps, an unbroken run Chromium char-breaks, and
  // multi-space runs, which hang at the edge.
  const prose = 'wrap parity alpha beta gamma delta epsilon zeta eta theta '.repeat(20);
  const longWord = 'x'.repeat(300);
  await gw.typeText(prose + '\n' + longWord + '\nshort  double  spaces');
  await gw.waitIdle();

  const taRows = await window.evaluate(() => {
    const ta = document.getElementById('gw-text-editor') as HTMLTextAreaElement;
    const cs = getComputedStyle(ta);
    const lineH = parseFloat(cs.lineHeight);
    const pad = parseFloat(cs.paddingTop) + parseFloat(cs.paddingBottom);
    // scrollHeight is the larger of content and box, so collapse the box for a
    // beat; the per-frame sync restores it.
    const prevH = ta.style.height;
    ta.style.height = '0px';
    const sh = ta.scrollHeight;
    ta.style.height = prevH;
    return Math.round((sh - pad) / lineH);
  });
  const canvasRows = await window.evaluate(() => (window as any).__gridwellTest.rawRows());
  expect(taRows, 'the content genuinely wraps').toBeGreaterThan(6);
  // scrollHeight is integer device px, so one row of rounding slack. An
  // unwrapped painter is off by dozens.
  expect(
    Math.abs(canvasRows - taRows),
    `canvas rows ${canvasRows} must match textarea rows ${taRows}`,
  ).toBeLessThanOrEqual(1);
});
