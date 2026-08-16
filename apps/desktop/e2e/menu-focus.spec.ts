import { test, expect } from './fixtures';

// Regression guard for the "+" creation menu's focused-pane invariant (CLAUDE.md:
// "the menu appears on exactly one pane, whichever is focused"; ARCHITECTURE.md
// invariant I10). The owner's recurring report was the menu lingering on a pane
// that was no longer focused — the menu's open flag used to be cleared from ~14
// scattered sites with no owner, so a focus path could forget to close it.
//
// client/menu now owns the rule (SyncFocus closes the menu when focus leaves its
// pane) and is unit-tested headlessly; this spec proves the wired-up behavior end
// to end through the real app: the live menu must not survive a focus excursion.

test('the + menu closes when focus leaves its pane', async ({ gw }) => {
  await gw.enterPlugin('local');
  await gw.splitFocusedPaneVertical();
  const panes = await gw.panes();
  expect(panes.length, 'split produced two panes').toBe(2);
  const [left, right] = panes.slice().sort((a, b) => a.x - b.x);

  // Open the menu on the RIGHT pane: its + button sits at the window edge, clear
  // of the divider (the left pane's + would overlap the divider's resize band, a
  // separate geometry issue). Focus it first so the single + click toggles open.
  await gw.focusPane(right);
  await gw.openPalette();
  expect((await gw.palette()).open, 'menu opened on the focused (right) pane').toBe(true);

  // Move focus to the left pane (the menu must close), then return focus to the
  // right pane. If the menu had been left stranded open, palette().open — which
  // reports the focused pane — would now read true.
  await gw.focusPane(left);
  await gw.focusPane(right);
  expect(
    (await gw.palette()).open,
    'the menu must not survive focus leaving and returning to its pane',
  ).toBe(false);
});
