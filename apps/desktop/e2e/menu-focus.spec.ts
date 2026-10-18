import { test, expect } from './fixtures';

// The + menu appears on exactly one pane, whichever is focused. client/menu's
// SyncFocus owns the rule; this proves the wiring end to end.

test('the + menu closes when focus leaves its pane', async ({ gw }) => {
  await gw.enterPlugin('home');
  await gw.splitFocusedPaneVertical();
  const panes = await gw.panes();
  expect(panes.length, 'split produced two panes').toBe(2);
  const [left, right] = panes.slice().sort((a, b) => a.x - b.x);

  // The right pane's + sits at the window edge, clear of the divider's resize
  // band, which the left pane's would overlap.
  await gw.focusPane(right);
  await gw.openMenu();
  expect((await gw.menu()).open, 'menu opened on the focused (right) pane').toBe(true);

  // menu() reports the focused pane, so a stranded menu reads open after
  // focus leaves and comes back.
  await gw.focusPane(left);
  await gw.focusPane(right);
  expect(
    (await gw.menu()).open,
    'the menu must not survive focus leaving and returning to its pane',
  ).toBe(false);
});
