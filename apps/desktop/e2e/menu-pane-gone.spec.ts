import type { ElectronApplication, Page } from '@playwright/test';
import { test, expect } from './fixtures';
import type { GridwellDriver } from './driver';
import { PARK_COORD } from '../src/main/viewutil';

// The + menu belongs to the focused pane: once its pane loses focus or is
// gone, by any path, it is closed, so it parks no live view for a menu no
// pane draws (menu.State.SyncFocus, applied at every draw). The paths here
// move focus without a press on a pane: a crush that closes the menu's pane,
// and a split that focuses the new pane.

// Two stacked panes, the lower live on a url from the node's own origin.
async function liveBelow(electronApp: ElectronApplication, window: Page, gw: GridwellDriver) {
  await gw.enterSource('home');
  await gw.splitFocusedPaneHorizontal();
  const stacked = (await gw.panes()).slice().sort((a, b) => a.y - b.y);
  expect(stacked.length, 'the split made two stacked panes').toBe(2);
  await gw.focusPane(stacked[1]);
  await gw.clickMenuSwatch('url');
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', `${gw.origin}/?menu-pane-gone=1`);
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();
  await expect.poll(() => parkedX(electronApp, stacked[1].id), { timeout: 15_000 }).not.toBeUndefined();
  await gw.focusPane(stacked[0]);
  return { upper: stacked[0], lower: stacked[1] };
}

function parkedX(electronApp: ElectronApplication, paneID: string): Promise<number | undefined> {
  return electronApp.evaluate((_, id) => (globalThis as any).__gwRegistry?.viewBoundsFor(id)?.x, paneID);
}

test('the + menu closes with its pane when a crush closes it', async ({ electronApp, window, gw }) => {
  const { upper, lower } = await liveBelow(electronApp, window, gw);
  await gw.openMenu();
  expect((await gw.menu()).open, 'the menu opened on the upper pane').toBe(true);
  await expect.poll(() => parkedX(electronApp, lower.id), { message: 'the open menu parks the live view' }).toBe(PARK_COORD);

  // A left-drag on the divider, up past the wall, crushes the upper pane.
  const x = upper.x + upper.w / 2;
  const m = window.mouse;
  await m.move(x, upper.y + upper.h - 2);
  await m.down();
  await m.move(x, upper.y + upper.h - 10);
  await m.move(x, upper.y + 2, { steps: 10 });
  await m.up();
  await gw.waitIdle();

  const panes = await gw.panes();
  expect(panes.map((p) => p.id), 'the crush closed the upper pane').toEqual([lower.id]);
  await expect
    .poll(() => parkedX(electronApp, lower.id), { message: 'no menu is left to park the live view' })
    .not.toBe(PARK_COORD);
  await expect
    .poll(() => window.evaluate(() => (window as any).__gridwellTest.keyboard().owner))
    .toBe('page');
});

test('the + menu closes when a split moves focus off its pane', async ({ electronApp, window, gw }) => {
  const { upper, lower } = await liveBelow(electronApp, window, gw);
  await gw.openMenu();
  await expect.poll(() => parkedX(electronApp, lower.id), { message: 'the open menu parks the live view' }).toBe(PARK_COORD);

  // A right-drag in from the upper pane's right edge splits it, focusing the
  // new pane.
  const y = upper.y + upper.h / 2;
  await gw.rightDragScreen(upper.x + upper.w - 5, y, upper.x + upper.w * 0.45, y);
  expect((await gw.panes()).length, 'the split made a third pane').toBe(3);
  expect((await gw.focused()).id, 'focus moved off the menu pane').not.toBe(upper.id);
  await expect
    .poll(() => parkedX(electronApp, lower.id), { message: 'no menu is left to park the live view' })
    .not.toBe(PARK_COORD);
});
