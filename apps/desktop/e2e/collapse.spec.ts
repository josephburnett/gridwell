import { test, expect } from './fixtures';

// Proves the god-object consolidation's lifecycle end of things: when a pane is
// collapsed, forgetPane tears down its per-pane state (a.locals entry) rather
// than leaking it. Also the first e2e to exercise the pane-collapse gesture at
// all (previously zero coverage of the collapse path).
test('collapsing a pane tears down its per-pane state (forgetPane)', async ({ gw, window }) => {
  await gw.enterPlugin('home');

  // Give the focused pane per-pane state. Since S8 the pane's PLACE lives on
  // the pane itself, so a.locals holds only the selection and the live
  // surfaces — an ephemeral url visit is the quickest of those: it descends
  // and opens a native view, which is exactly the state forgetPane must tear
  // down (not merely drop a map entry for).
  await gw.clickPaletteSwatch('url');
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', 'https://example.com/collapse');
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  const origId = (await gw.focused()).id;
  expect(await gw.localPaneIds(), 'the pane has per-pane state before the split').toContain(origId);

  // Split (the original keeps its id + state on the LEFT), then collapse it.
  await gw.splitFocusedPaneVertical();
  expect((await gw.panes()).length, 'split produced two panes').toBe(2);
  await gw.collapseLeftPane();

  // The left pane is gone, and its per-pane state went with it — not orphaned.
  expect((await gw.panes()).length, 'collapsed back to one pane').toBe(1);
  expect(await gw.localPaneIds(), "the collapsed pane's per-pane state was torn down").not.toContain(origId);
});
