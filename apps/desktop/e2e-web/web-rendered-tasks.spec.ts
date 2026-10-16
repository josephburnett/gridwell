import { test, expect } from './fixtures';
import { tileAt } from '../e2e/oracle';

// An unfocused rendered pane paints a picture of its document, and a picture
// holds no form control: iOS WebKit paints an SVG image whose foreignObject
// holds an <input> as an empty box, though the image loads. A task list
// pictures its checkboxes as stand-ins the one stylesheet draws
// (markdown.PictureXHTML), its checked state carried.
test('an unfocused rendered task list paints a picture with no form control', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const created = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;
  await gw.descendCell(cx, cy);
  await gw.pasteText('- [ ] a\n- [x] b');

  await gw.toggleTextMode();
  await expect.poll(async () => (await gw.focused()).textMode).toBe('rendered');
  await expect(window.locator('#gw-rendered-view input[type=checkbox]')).toHaveCount(2);

  const orig = (await gw.panes()).find((p: any) => p.textFocus === created.id)!;
  await gw.splitFocusedPaneVertical();
  await gw.waitIdle();

  const face = async () =>
    ((await window.evaluate(() => (window as any).__gridwellTest.paintedText())) as Record<string, string>)[
      'pane:' + orig.id
    ] ?? '';
  await expect.poll(face, { timeout: 15_000 }).toMatch(/^raster:/);
  const svg = await face();
  expect(svg).not.toContain('<input');
  expect(svg.match(/<span class="gw-task"><\/span>/g) ?? []).toHaveLength(1);
  expect(svg.match(/<span class="gw-task gw-task-done"><\/span>/g) ?? []).toHaveLength(1);
});
