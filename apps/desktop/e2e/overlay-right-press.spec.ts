import { test, expect } from './fixtures';

// A DOM overlay over a pane — the text editor's textarea, the shell's xterm
// host — hit-tests before the canvas, so a right press on one reaches Gridwell
// only because the overlay forwards it. Both forward through the same
// installer, so this spec drives the one gesture that proves the forward
// arrived: the right-drag that splits the pane. A split needs the press
// classified against the pane rect, so a forward that lost the coordinates or
// the button fails here too.

// A right-drag inward from the pane's left third: outside the swap third and
// outside the resize band, so it classifies as a split, and door inside the
// overlay, which stops short of the pane edge by a few pixels.
async function rightDragSplitFromOverlay(gw: any): Promise<number> {
  const p = await gw.focused();
  const y = p.y + p.h / 2;
  await gw.rightDragScreen(p.x + p.w * 0.15, y, p.x + p.w * 0.5, y);
  return (await gw.panes()).length;
}

test('a right-drag started on the text overlay splits the pane', async ({ gw }) => {
  await gw.enterSource('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  await gw.openMenu();
  await gw.dragCreate('markdown', cx, cy);
  await gw.descendCell(cx, cy);
  await expect
    .poll(async () => (await gw.textareaInfo()) != null, { timeout: 10_000 })
    .toBe(true);

  expect(await rightDragSplitFromOverlay(gw), 'the press over the textarea armed the split').toBe(2);
});

test('a right-drag started on the shell overlay splits the pane', async ({ gw }) => {
  await gw.enterSource('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  await gw.openMenu();
  await gw.dragCreate('shell', cx, cy);
  // The drop lands bare; the descent opens the tmux and the xterm host.
  await gw.descendCell(cx, cy);
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');

  expect(await rightDragSplitFromOverlay(gw), 'the press over the xterm host armed the split').toBe(2);
});

// A pane gesture's preview is drawn on the canvas, under every overlay, so
// the overlay over the pane parks while the gesture is armed
// (pane.ParkSurface), as a live url view and a shell do; the text is drawn on
// the canvas meanwhile. The raw editor parks without losing the keyboard, so
// an Esc that cancels the drag leaves the user typing where they were.
async function holdRightDrag(gw: any, window: any): Promise<{ x: number; y: number }> {
  const p = await gw.focused();
  const y = Math.round(p.y + p.h / 2);
  const from = Math.round(p.x + p.w * 0.15);
  const to = Math.round(p.x + p.w * 0.5);
  await window.mouse.move(from, y);
  await window.mouse.down({ button: 'right' });
  await window.mouse.move(to, y, { steps: 10 });
  await gw.waitIdle();
  return { x: to, y };
}

const topAt = (window: any, pt: { x: number; y: number }) =>
  window.evaluate(([x, y]: number[]) => {
    const el = document.elementFromPoint(x, y);
    return el ? el.tagName + (el.id ? '#' + el.id : '') : '';
  }, [pt.x, pt.y]);

for (const mode of ['raw', 'rendered']) {
  test(`a right-drag over a ${mode} text pane shows its preview and lands`, async ({ gw, window }) => {
    await gw.enterSource('home');
    const f = await gw.focused();
    const cx = Math.round(f.cx);
    const cy = Math.round(f.cy);

    await gw.openMenu();
    await gw.dragCreate('markdown', cx, cy);
    await gw.descendCell(cx, cy);
    await expect.poll(async () => (await gw.textareaInfo()) != null, { timeout: 10_000 }).toBe(true);
    await gw.pasteText('# Title\n\n' + 'a line of text\n'.repeat(30));
    const overlay = mode === 'raw' ? '#gw-text-editor' : '#gw-rendered-view';
    if (mode === 'rendered') {
      await gw.toggleTextMode();
      await expect(window.locator(overlay)).toBeVisible();
    }

    const pt = await holdRightDrag(gw, window);
    const rd = await window.evaluate(() => (window as any).__gridwellTest.rightDrag());
    expect(rd, 'the press over the overlay armed the gesture').not.toBeNull();
    expect(rd.x, 'the preview follows the pointer').toBe(pt.x);
    expect(await topAt(window, pt), 'nothing covers the preview').toBe('CANVAS#canvas');

    if (mode === 'raw') {
      await window.keyboard.press('Escape');
      await gw.waitIdle();
      expect(await topAt(window, pt), 'the editor is back').toBe('TEXTAREA#gw-text-editor');
      expect(
        await window.evaluate(() => document.activeElement?.id),
        'the editor kept the keyboard through the park',
      ).toBe('gw-text-editor');
      await window.mouse.up({ button: 'right' });
      await gw.waitIdle();
      expect((await gw.panes()).length, 'the cancelled drag split nothing').toBe(1);
      return;
    }
    await window.mouse.up({ button: 'right' });
    await gw.waitIdle();
    expect((await gw.panes()).length, 'the drag split the pane').toBe(2);
  });
}
