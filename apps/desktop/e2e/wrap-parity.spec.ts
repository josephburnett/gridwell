import { test, expect } from './fixtures';

// Raw text must not reflow when pane focus moves. The canvas painter, which is
// what an unfocused descended pane shows, soft-wraps to the same columns the
// editing textarea does. This spec crosses the browser-wrap and canvas-wrap seam
// with the same bytes on both sides: the textarea's own rendered row count, its
// scrollHeight over its line box, must match the rows the canvas painter
// computes, read through the rawRows hook. A painter that draws one row per
// source line diverges by dozens of rows on wrapping prose, which is the visible
// unwrap.

test('the canvas paints the rows the textarea soft-wraps', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  await gw.descendCell(cx, cy);
  await expect.poll(async () => (await gw.focused()).textFocus).not.toBe('');

  // A paragraph that soft-wraps many times, an unbroken run longer than any line,
  // which Chromium's textarea break-word char-breaks, and multi-space runs, which
  // hang at the edge.
  const prose = 'wrap parity alpha beta gamma delta epsilon zeta eta theta '.repeat(20);
  const longWord = 'x'.repeat(300);
  await gw.typeText(prose + '\n' + longWord + '\nshort  double  spaces');
  await gw.waitIdle();

  const taRows = await window.evaluate(() => {
    const ta = document.getElementById('gw-text-editor') as HTMLTextAreaElement;
    const cs = getComputedStyle(ta);
    const lineH = parseFloat(cs.lineHeight);
    const pad = parseFloat(cs.paddingTop) + parseFloat(cs.paddingBottom);
    // scrollHeight is the larger of the content and the box, so collapse the box
    // for a beat to read pure content height; the per-frame sync restores it.
    const prevH = ta.style.height;
    ta.style.height = '0px';
    const sh = ta.scrollHeight;
    ta.style.height = prevH;
    return Math.round((sh - pad) / lineH);
  });
  const canvasRows = await window.evaluate(() => (window as any).__gridwellTest.rawRows());
  // Three source lines must have wrapped into many visual rows.
  expect(taRows, 'the content genuinely wraps').toBeGreaterThan(6);
  // scrollHeight is integer device px, so allow one row of rounding slack. An
  // unwrapped painter is off by dozens and still fails.
  expect(
    Math.abs(canvasRows - taRows),
    `canvas rows ${canvasRows} must match textarea rows ${taRows}`,
  ).toBeLessThanOrEqual(1);
});
