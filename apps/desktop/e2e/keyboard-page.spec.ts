import { test, expect } from './fixtures';
import type { PaneInfo } from './driver';
import { createText, getTileContent } from './oracle';

// The keyboard's owner inside the root page: the one element typing goes to
// follows the focused pane whatever moved it, and a focus that lands anywhere
// else with no press behind it goes back.

const paneOf = async (gw: any, id: string): Promise<PaneInfo> => (await gw.panes()).find((p: PaneInfo) => p.id === id)!;

test('crushing a focused raw text beside another types into the survivor, not the closed tile', async ({ gw, window }) => {
  await gw.enterSource('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  const left = await createText(gw.origin, f.gridID, cx, cy, 'left');
  const right = await createText(gw.origin, f.gridID, cx + 1, cy, 'right');
  await gw.descendCell(cx, cy);
  await expect.poll(() => gw.textareaValue(), { timeout: 10_000 }).toBe('left');
  const leftPane = (await gw.focused()).id;
  // The split's new pane ascends and descends into the other text.
  await gw.splitFocusedPaneVertical();
  const rightPane = (await gw.focused()).id;
  await gw.middleClickPane();
  await gw.descendCell(cx + 1, cy);
  await expect.poll(() => gw.textareaValue(), { timeout: 10_000 }).toBe('right');

  // A press on the right pane's side of the divider, dragged across it to
  // the window's edge, crushes the focused pane.
  const r = await paneOf(gw, rightPane);
  const gy = r.y + r.h / 2;
  await gw.leftDragScreen(r.x + 2, gy, r.x + r.w - 2, gy);
  await expect.poll(async () => (await gw.panes()).length).toBe(1);
  expect((await gw.focused()).id).toBe(leftPane);
  await expect.poll(() => gw.textareaValue()).toBe('left');

  await window.keyboard.press('End');
  await gw.typeText('!');
  await expect.poll(() => getTileContent(gw.origin, left.id), { timeout: 10_000 }).toBe('left!');
  expect(await getTileContent(gw.origin, right.id), 'the closed tile took no typing').toBe('right');
});

test('Tab in a raw editor keeps the keyboard there, away from a live shell beside it', async ({ gw, window }) => {
  await gw.enterSource('home');
  await gw.splitFocusedPaneVertical();
  const [lp, rp] = (await gw.panes()).slice().sort((a: PaneInfo, b: PaneInfo) => a.x - b.x);
  await gw.focusPane(lp);
  const lf = await gw.focused();
  const cx = Math.round(lf.cx);
  const cy = Math.round(lf.cy);
  await gw.openMenu();
  await gw.dragCreate('shell', cx, cy);
  await gw.descendCell(cx, cy);
  await gw.shellAttached();
  const shellPane = (await gw.focused()).id;

  await gw.focusPane(await paneOf(gw, rp.id));
  const rf = await gw.focused();
  const text = await createText(gw.origin, rf.gridID, Math.round(rf.cx) + 2, Math.round(rf.cy), 'tab');
  await gw.descendCell(Math.round(rf.cx) + 2, Math.round(rf.cy));
  await expect.poll(() => gw.textareaValue(), { timeout: 10_000 }).toBe('tab');
  const inEditor = () => window.evaluate(() => document.activeElement?.id === 'gw-text-editor');
  await expect.poll(inEditor).toBe(true);

  await window.keyboard.press('Tab');
  await expect.poll(inEditor, { message: 'the editor kept the keyboard' }).toBe(true);
  await window.keyboard.press('End');
  await gw.typeText('Z');
  await expect.poll(() => getTileContent(gw.origin, text.id), { timeout: 10_000 }).toBe('tabZ');
  await gw.focusPane(await paneOf(gw, shellPane));
  const shellText: string = await window.evaluate(() => (window as any).__gridwellTest.shellText());
  expect(shellText, 'the shell received nothing').not.toContain('Z');
  await gw.ascendViaCrumb();
  await gw.deleteTileCell(cx, cy);
});
