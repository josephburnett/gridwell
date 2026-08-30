import { test, expect } from './fixtures';

// The rendered view is a sanitized-HTML overlay div fed by goldmark through
// markdown.RenderHTML. It is read-only: the raw textarea is the one editor, and
// the toggle flips between them. This spec crosses the whole seam: source bytes
// typed in the editor come back as real HTML elements in #gw-rendered-view, and
// the toggle round-trips.

test('rendered mode shows sanitized HTML; the toggle round-trips to the editor', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  await gw.descendCell(cx, cy);
  await gw.typeText('# Big Title\n\nhello *world* <script>alert(1)</script>');
  await gw.toggleTextMode(); // rendered
  await gw.waitIdle();

  const view = window.locator('#gw-rendered-view');
  await expect(view).toBeVisible();
  await expect(view.locator('h1')).toHaveText('Big Title');
  await expect(view.locator('em')).toHaveText('world');
  // Sanitization: the script tag must not exist as an element.
  await expect(view.locator('script')).toHaveCount(0);
  // The editor is hidden while rendered.
  await expect(window.locator('#gw-text-editor')).toBeHidden();

  // Toggle back: the editor returns with the same source and the overlay hides.
  await gw.toggleTextMode();
  await expect(window.locator('#gw-text-editor')).toBeVisible();
  await expect(view).toBeHidden();
  const val = await window.evaluate(
    () => (document.getElementById('gw-text-editor') as HTMLTextAreaElement).value,
  );
  expect(val).toContain('# Big Title');
});
