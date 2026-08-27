import { test, expect } from './fixtures';

// Crosses the plugin-classification seam (issue #47): a plugin whose Info
// handshake succeeds but declares no root grid ("rootless" — e.g. an fs
// plugin with no config.root) must be visibly distinct from — and behave
// differently from — an enterable one. Before this fix, both a broken and a
// rootless plugin rendered as an ordinary tile whose click silently did
// nothing. The surface moved from the launcher landing page to the + menu's
// plugin row when the launcher decision was reversed (2026-07-19); the
// classification and the click-notice contract are unchanged.
//
// The broken-plugin case (Info failing/timing out) is NOT covered here: a
// plugin that fails to spawn at all aborts the whole server by design (see
// cmd/gridwell), so there is no way to reach a "broken" plugin swatch in a
// real app boot. That case is covered at the buildPluginInfo/pluginInfo unit
// level (internal/server/plugininfo_test.go).
test.use({ extraPlugins: [{ kind: 'fs', name: 'rootless' }] });

test('a rootless plugin swatch is inert and reports a notice instead of descending', async ({ gw, window }) => {
  const pls = await gw.plugins();
  const rootless = pls.find((p) => p.label === 'rootless');
  expect(rootless, 'rootless fs plugin configured').toBeTruthy();
  expect(rootless!.rootGridID, 'a rootless plugin has no root grid').toBe('');
  expect(rootless!.status, 'classified as rootless, not broken or enterable').toBe('rootless');
  expect(rootless!.infoError, 'a rootless (not broken) plugin carries no InfoError').toBe('');

  // Boot skipped the rootless plugin: home is the FIRST plugin WITH a root.
  const before = await gw.focused();
  expect(before.anchor, 'boots into the first rooted plugin, not the fs plugin').toBe(
    pls.find((p) => p.label === 'e2e')!.rootGridID,
  );

  // The + menu shows the rootless plugin's swatch with its classification
  // (the tint the render path reads); click it.
  await gw.openPalette();
  const pal = await gw.palette();
  const swatch = pal.items.find((i) => i.isPlugin && i.label === 'rootless');
  expect(swatch, 'rootless plugin swatch in the + menu').toBeTruthy();
  expect(swatch!.status, 'swatch carries the pluginhealth classification').toBe('rootless');
  await window.mouse.click(swatch!.x + swatch!.w / 2, swatch!.y + swatch!.h / 2);
  await gw.waitIdle();

  // It must NOT have descended: the focused pane is still anchored at home,
  // not inside the fs plugin's root.
  const after = await gw.focused();
  expect(after.anchor, 'click on a rootless swatch must not descend').toBe(before.anchor);

  // Instead, a notice must have appeared, attributed to this plugin by UUID
  // (ClickNotice's source key — labels can collide across connections), as
  // an Info severity (a fixable configuration gap, not a failure)
  // mentioning the fix.
  const rootlessRow = pls.find((p) => p.label === 'rootless')!;
  const errs = await window.evaluate(() => (window as any).__gridwellTest.errors());
  const notice = errs.notices.find((n: any) => n.source === 'launcher:' + rootlessRow.uuid);
  expect(notice, 'a notice keyed by the plugin uuid is present').toBeTruthy();
  expect(notice.severity).toBe('info');
  expect(notice.message).toContain('no root configured');
});
