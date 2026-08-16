import { test, expect } from './fixtures';

// Issue #216: raw text must not reflow when pane focus moves. The canvas
// painter (what an unfocused descended pane shows) soft-wraps to the same
// columns the editing textarea does. This spec crosses the browser-wrap vs
// canvas-wrap seam with the SAME bytes on both sides: the textarea's own
// rendered row count (scrollHeight over its line box) must match the rows
// the canvas painter computes (the rawRows hook). Before the fix the canvas
// painted one row per source line — for wrapping prose the two counts
// diverged by dozens of rows, which is exactly the visible "unwrap".

test('the canvas paints the rows the textarea soft-wraps', async ({ gw, window }) => {
  await gw.enterPlugin('local');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  await gw.descendCell(cx, cy);
  await expect.poll(async () => (await gw.focused()).textFocus).not.toBe('');

  // A paragraph that soft-wraps many times, an unbroken run longer than any
  // line (Chromium's textarea break-word char-breaks it), and multi-space
  // runs (they hang at the edge).
  const prose = 'wrap parity alpha beta gamma delta epsilon zeta eta theta '.repeat(20);
  const longWord = 'x'.repeat(300);
  await gw.typeText(prose + '\n' + longWord + '\nshort  double  spaces');
  await gw.waitIdle();

  const taRows = await window.evaluate(() => {
    const ta = document.getElementById('gw-text-editor') as HTMLTextAreaElement;
    const cs = getComputedStyle(ta);
    const lineH = parseFloat(cs.lineHeight);
    const pad = parseFloat(cs.paddingTop) + parseFloat(cs.paddingBottom);
    // scrollHeight is max(content, box): collapse the box for a beat so it
    // reports pure content height (the per-frame sync restores it anyway).
    const prevH = ta.style.height;
    ta.style.height = '0px';
    const sh = ta.scrollHeight;
    ta.style.height = prevH;
    return Math.round((sh - pad) / lineH);
  });
  const canvasRows = await window.evaluate(() => (window as any).__gridwellTest.rawRows());
  // Three source lines must have wrapped into many visual rows.
  expect(taRows, 'the content genuinely wraps').toBeGreaterThan(6);
  // scrollHeight is integer device px, so allow one row of rounding slack —
  // an unwrapped painter is off by dozens, which this still catches cold.
  expect(
    Math.abs(canvasRows - taRows),
    `canvas rows ${canvasRows} must match textarea rows ${taRows}`,
  ).toBeLessThanOrEqual(1);
});
