import { test, expect } from './fixtures';

// The + creation menu appears on exactly one pane, whichever is focused. Clear
// its open flag from many scattered sites and some focus path forgets, leaving
// the menu on a pane that is no longer focused.
//
// client/menu owns the rule: SyncFocus closes the menu when focus leaves its
// pane, and is unit-tested headlessly. This spec proves the wiring end to end
// through the real app: the live menu must not survive a focus excursion.

test('the + menu closes when focus leaves its pane', async ({ gw }) => {
  await gw.enterPlugin('home');
  await gw.splitFocusedPaneVertical();
  const panes = await gw.panes();
  expect(panes.length, 'split produced two panes').toBe(2);
  const [left, right] = panes.slice().sort((a, b) => a.x - b.x);

  // Open the menu on the right pane: its + button sits at the window edge, clear
  // of the divider, while the left pane's + would overlap the divider's resize
  // band. Focus it first so a single + click toggles it open.
  await gw.focusPane(right);
  await gw.openPalette();
  expect((await gw.palette()).open, 'menu opened on the focused (right) pane').toBe(true);

  // Move focus to the left pane, which must close the menu, then return focus to
  // the right pane. A menu left stranded open would show as palette().open,
  // which reports the focused pane, reading true.
  await gw.focusPane(left);
  await gw.focusPane(right);
  expect(
    (await gw.palette()).open,
    'the menu must not survive focus leaving and returning to its pane',
  ).toBe(false);
});
