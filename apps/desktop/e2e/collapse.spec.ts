import { test, expect } from './fixtures';

// Collapsing a pane tears down its per-pane state: forgetPane drops the
// a.locals entry. This is also the e2e coverage of the pane-collapse gesture.
test('collapsing a pane tears down its per-pane state (forgetPane)', async ({ gw, window }) => {
  await gw.enterPlugin('home');

  // a.locals holds only the selection and the live surfaces. An ephemeral url
  // visit opens a native view, so it gives the pane state forgetPane has to
  // tear down and not just drop a map entry for.
  await gw.clickMenuSwatch('url');
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', 'https://example.com/collapse');
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  const origId = (await gw.focused()).id;
  expect(await gw.localPaneIds(), 'the pane has per-pane state before the split').toContain(origId);

  // The split keeps the original pane's id and state on the left.
  await gw.splitFocusedPaneVertical();
  expect((await gw.panes()).length, 'split produced two panes').toBe(2);
  await gw.collapseLeftPane();

  expect((await gw.panes()).length, 'collapsed back to one pane').toBe(1);
  expect(await gw.localPaneIds(), "the collapsed pane's per-pane state was torn down").not.toContain(origId);
});
