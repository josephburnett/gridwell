import { test as base, expect } from './fixtures';

// Crosses the launcher-classification seam (issue #47): a plugin whose Info
// handshake succeeds but declares no root grid ("rootless" — e.g. an fs
// plugin with no config.root) must be visibly distinct from — and behave
// differently from — an enterable one. Before this fix, both a broken and a
// rootless plugin rendered as an ordinary launcher tile whose click silently
// did nothing (enterPlugin bailed at RootGridID == "" with no signal).
//
// The broken-plugin case (Info failing/timing out) is NOT covered here: a
// plugin that fails to spawn at all aborts the whole server by design (see
// cmd/gridwell), so there is no way to reach a "broken" launcher tile in a
// real app boot. That case is covered at the buildPluginInfo/pluginInfo unit
// level (internal/server/plugininfo_test.go).
const test = base.extend<Record<string, never>>({});
test.use({ extraPlugins: [{ kind: 'fs', name: 'rootless' }] });

test('a rootless plugin tile is inert and reports a notice instead of descending', async ({ gw, window }) => {
  // The hook installs before ListPlugins resolves; wait for both launcher
  // tiles (localdb "e2e" + fs "rootless") to appear.
  await window.waitForFunction(() => (window as any).__gridwellTest.launcher().length >= 2, null, {
    timeout: 15_000,
  });
  const tiles = await gw.launcher();
  const rootless = tiles.find((t) => t.label === 'rootless');
  expect(rootless, 'rootless fs plugin tile present in the launcher').toBeTruthy();
  expect(rootless!.rootGridID, 'a rootless plugin has no root grid').toBe('');
  expect(rootless!.status, 'classified as rootless, not broken or enterable').toBe('rootless');
  expect(rootless!.infoError, 'a rootless (not broken) plugin carries no InfoError').toBe('');

  const before = await gw.focused();
  expect(before.anchor, 'starts at the launcher (no plugin entered)').toBe('');

  // Click the rootless tile.
  await window.mouse.click(rootless!.x, rootless!.y);
  await gw.waitIdle();

  // It must NOT have descended: the focused pane is still at the launcher
  // anchor, not inside the fs plugin's root.
  const after = await gw.focused();
  expect(after.anchor, 'click on a rootless tile must not descend').toBe('');

  // Instead, a notice must have appeared, attributed to this plugin by label
  // (client/pluginhealth.ClickNotice's source key), as an Info severity (a
  // fixable configuration gap, not a failure) mentioning the fix.
  const errs = await window.evaluate(() => (window as any).__gridwellTest.errors());
  const notice = errs.notices.find((n: any) => n.source === 'plugin:rootless');
  expect(notice, 'a notice for plugin:rootless is present').toBeTruthy();
  expect(notice.severity).toBe('info');
  expect(notice.message).toContain('no root configured');

  // And the launcher tile itself carries the visible dimming — proven
  // indirectly here via the classification the render path reads (status);
  // the pixel-level tint is exercised by client/wasm's own build + the
  // classification's unit tests (client/pluginhealth).
  expect(rootless!.status).toBe('rootless');
});
