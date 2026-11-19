import type { Page } from '@playwright/test';
import { test, expect } from './fixtures';
import type { GridwellDriver, PaneInfo } from './driver';
import { createText, createTile, handshake, tileAt } from './oracle';

// A new workspace tile's capture is the whole view, ephemeral visits included
// (Joe, 2026-11-18: "They should be captured too. Everything should look
// exactly the same."). A visit lives while a pane, on screen or parked, or a
// workspace tile's layout holds it, and goes when the last of them lets go.

const depth = (window: Page): Promise<number> =>
  window.evaluate(() => (window as any).__gridwellTest.workspace().depth);

// What a pane shows, without the level-scoped pane id.
const place = (p: PaneInfo) => ({ anchor: p.anchor, path: p.path, textFocus: p.textFocus, gridID: p.gridID, placeDepth: p.placeDepth });

// Panes in reading order: top to bottom, then left to right.
const ordered = async (gw: GridwellDriver): Promise<PaneInfo[]> =>
  (await gw.panes()).slice().sort((a, b) => a.y - b.y || a.x - b.x);

// The bar's title for each pane, read by focusing it with a plain click at
// its center, which a content pane takes as a press on its surface.
async function titles(window: Page, gw: GridwellDriver): Promise<string[]> {
  const out: string[] = [];
  for (const p of await ordered(gw)) {
    await window.mouse.click(p.x + p.w / 2, p.y + p.h / 2);
    await gw.waitIdle();
    await expect.poll(async () => (await gw.focused()).id).toBe(p.id);
    out.push((await gw.barName()).label);
  }
  return out;
}

const scratchIDs = async (gw: GridwellDriver, scratch: string): Promise<string[]> =>
  ((await gw.getGrid(scratch)).tiles ?? []).map((t) => String(t.id));

// Focuses the pane with a plain click, then ascends it one frame by its crumb.
async function ascendPane(window: Page, gw: GridwellDriver, p: PaneInfo): Promise<void> {
  await window.mouse.click(p.x + p.w / 2, p.y + p.h / 2);
  await gw.waitIdle();
  await expect.poll(async () => (await gw.focused()).id).toBe(p.id);
  await gw.ascendViaCrumb();
}

test('a new workspace tile captures every pane on a visit as it is, and the visit lives while anything holds it', async ({
  electronApp,
  window,
  gw,
}) => {
  const origin = new URL(window.url()).origin;
  await gw.enterSource('home');
  const scratch = await gw.scratchGridID('home');
  const p0 = await gw.focused();
  const rootGrid = p0.gridID;

  // A door template, whose click visits an ephemeral copy of it.
  const templates = (await handshake(origin)).templatesGridId ?? '';
  expect(templates, 'the handshake names the templates grid').not.toBe('');
  const doorTemplate = await createTile(origin, templates, 'door', 0, 0);

  // Pane 1: an ephemeral url visit.
  await gw.splitFocusedPaneVertical();
  const wcBefore = await electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length);
  await gw.clickMenuSwatch('url');
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?capture=1`);
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), { timeout: 15_000 })
    .toBeGreaterThan(wcBefore);
  const urlVisit = (await gw.focused()).textFocus;
  expect(urlVisit, 'the url visit is a descent').not.toBe('');

  // Pane 2: an ephemeral shell visit.
  await gw.focusPane((await gw.panes()).find((p) => p.id === p0.id)!);
  await gw.splitFocusedPaneHorizontal();
  await gw.clickMenuSwatch('shell');
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  const shellVisit = (await gw.focused()).textFocus;

  // Pane 3: a door visit, and a text inside its grid, a frame deeper.
  await gw.focusPane((await gw.panes()).find((p) => p.id === p0.id)!);
  await gw.splitFocusedPaneHorizontal();
  await gw.openMenu();
  await gw.expandSources();
  let item: { x: number; y: number; w: number; h: number } | undefined;
  await expect
    .poll(async () => {
      item = (await gw.menu()).items.find((i) => i.template === doorTemplate.id);
      return !!item;
    }, { message: 'the strip shows the door template', timeout: 10_000 })
    .toBe(true);
  await window.mouse.click(item!.x + item!.w / 2, item!.y + item!.h / 2);
  await gw.waitIdle();
  const doorVisit = ((await gw.getGrid(scratch)).tiles ?? []).find((t) => t.kind === 'door')!;
  expect(doorVisit, 'the door visit is a copy on the scratch grid').toBeTruthy();
  await expect.poll(async () => (await gw.focused()).gridID).toBe(doorVisit.childGridId);
  const inside = await createText(origin, doorVisit.childGridId!, 0, 0, 'inside the door visit');
  await gw.waitClientTileAt((await gw.focused()).id, 0, 0, inside.id);
  await gw.descendCell(0, 0);
  await expect.poll(async () => (await gw.focused()).textFocus).toBe(inside.id);

  // The workspace tile, made and entered from pane 0, which shows the grid.
  await gw.focusPane((await gw.panes()).find((p) => p.id === p0.id)!);
  const f0 = await gw.focused();
  const wx = Math.round(f0.cx);
  const wy = Math.round(f0.cy);
  await gw.openMenu();
  await gw.dragCreate('workspace', wx, wy);
  const ws = tileAt(await gw.getGrid(rootGrid), 'workspace', wx, wy)!;
  expect(ws, 'the workspace tile was made').toBeTruthy();

  const before = (await ordered(gw)).map(place);
  const titlesBefore = await titles(window, gw);
  expect(titlesBefore.filter((t) => t.endsWith(' — ephemeral')).length, 'the url and shell visits say so').toBeGreaterThanOrEqual(2);
  await gw.focusPane((await gw.panes()).find((p) => p.id === p0.id)!);
  await gw.descendCell(wx, wy);
  await expect.poll(() => depth(window)).toBe(1);

  // Every pane shows the place it showed, the same frames of the same rows,
  // and wears the same title.
  expect((await ordered(gw)).map(place), 'the capture is the whole view').toEqual(before);
  expect(await titles(window, gw), 'and looks the same').toEqual(titlesBefore);

  // The first flush stores the visits in the layout.
  await expect
    .poll(async () => {
      try {
        const blob = await gw.getTileContent(ws.id);
        return [urlVisit, shellVisit, doorVisit.id, inside.id].every((id) => blob.includes(id.split('/').pop()!));
      } catch {
        return false;
      }
    }, { message: 'the stored layout names every visit', timeout: 10_000 })
    .toBe(true);
  for (const id of [urlVisit, shellVisit, doorVisit.id]) {
    expect(await scratchIDs(gw, scratch), `the visit ${id} is still a row`).toContain(id);
  }

  // Leaving the shell inside the workspace keeps it: the parked level shows it.
  const innerShell = (await gw.panes()).find((p) => p.textFocus === shellVisit)!;
  await ascendPane(window, gw, innerShell);
  await expect.poll(async () => (await gw.panes()).some((p) => p.textFocus === shellVisit)).toBe(false);
  expect(await scratchIDs(gw, scratch), 'a parked pane holds the shell').toContain(shellVisit);

  // Leaving the workspace retires nothing the outer panes show.
  await gw.leaveWorkspace();
  await expect.poll(() => depth(window)).toBe(0);
  expect((await ordered(gw)).map(place), 'the outer panes are as they were').toEqual(before);
  for (const id of [urlVisit, shellVisit, doorVisit.id]) {
    expect(await scratchIDs(gw, scratch), `leaving kept ${id}`).toContain(id);
  }

  // The outer shell pane was its last holder, so its ascent retires it.
  await ascendPane(window, gw, (await gw.panes()).find((p) => p.textFocus === shellVisit)!);
  await expect
    .poll(async () => (await scratchIDs(gw, scratch)).includes(shellVisit), { timeout: 10_000 })
    .toBe(false);

  // The workspace's layout still holds the url visit when the outer pane
  // leaves it, and the ascent inside the workspace, its last holder, retires it.
  await ascendPane(window, gw, (await gw.panes()).find((p) => p.textFocus === urlVisit)!);
  await expect.poll(async () => (await gw.panes()).some((p) => p.textFocus === urlVisit)).toBe(false);
  await gw.waitIdle();
  expect(await scratchIDs(gw, scratch), 'the layout holds the url visit').toContain(urlVisit);
  await gw.focusPane((await gw.panes()).find((p) => p.id === p0.id)!);
  await gw.descendCell(wx, wy);
  await expect.poll(() => depth(window)).toBe(1);
  await ascendPane(window, gw, (await gw.panes()).find((p) => p.textFocus === urlVisit)!);
  await expect
    .poll(async () => (await scratchIDs(gw, scratch)).includes(urlVisit), { timeout: 10_000 })
    .toBe(false);

  const e = await window.evaluate(() => (window as any).__gridwellTest.errors());
  expect(e.notices, 'nothing went wrong').toEqual([]);

  // A reload reopens the workspace from its layout, the door visit and the
  // text inside it included.
  await gw.waitIdle();
  const inner = (await ordered(gw)).map(place);
  expect(inner.some((p) => p.textFocus === inside.id), 'the door visit pane is still inside').toBe(true);
  await window.evaluate(([id]) => {
    location.href = `${location.origin}/?w=${encodeURIComponent(id)}&e2e=1`;
  }, [ws.id]);
  await window.waitForFunction(() => (window as any).__gridwellTest !== undefined);
  await expect.poll(() => depth(window), { timeout: 15_000 }).toBe(1);
  await expect.poll(async () => (await ordered(gw)).map(place), { timeout: 15_000 }).toEqual(inner);
  expect(await scratchIDs(gw, scratch), 'the door visit is still a row').toContain(doorVisit.id);
});
