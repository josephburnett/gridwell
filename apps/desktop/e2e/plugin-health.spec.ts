import { test, expect } from './fixtures';

// Crosses the plugin-classification seam: a plugin whose Info handshake succeeds
// but declares no root grid, such as an fs plugin with no config.root, must look
// and behave differently from an enterable one. Otherwise it renders as an
// ordinary swatch whose click silently does nothing.
//
// The broken-plugin case, where Info fails or times out, is not covered here: a
// plugin that fails to spawn aborts the whole server by design, in
// internal/plugin's loader, so no real app boot reaches a broken swatch. That
// case is covered at the buildPluginInfo and pluginInfo unit level in
// internal/server/plugininfo_test.go.
test.use({ extraPlugins: [{ kind: 'fs', name: 'rootless' }] });

test('a rootless plugin swatch is inert and reports a notice instead of descending', async ({ gw, window }) => {
  const pls = await gw.plugins();
  const rootless = pls.find((p) => p.label === 'rootless');
  expect(rootless, 'rootless fs plugin configured').toBeTruthy();
  expect(rootless!.rootGridID, 'a rootless plugin has no root grid').toBe('');
  expect(rootless!.status, 'classified as rootless, not broken or enterable').toBe('rootless');
  expect(rootless!.infoError, 'a rootless (not broken) plugin carries no InfoError').toBe('');

  // Boot skipped the rootless plugin and landed on home.
  const before = await gw.focused();
  expect(before.anchor, 'boots into the first rooted plugin, not the fs plugin').toBe(
    pls.find((p) => p.label === 'home')!.rootGridID,
  );

  // The + menu shows the rootless plugin's swatch with its classification, the
  // tint the render path reads. Click it.
  await gw.openPalette();
  const pal = await gw.palette();
  const swatch = pal.items.find((i) => i.isPlugin && i.label === 'rootless');
  expect(swatch, 'rootless plugin swatch in the + menu').toBeTruthy();
  expect(swatch!.status, 'swatch carries the pluginhealth classification').toBe('rootless');
  await window.mouse.click(swatch!.x + swatch!.w / 2, swatch!.y + swatch!.h / 2);
  await gw.waitIdle();

  // It must not have descended: the focused pane is still anchored at home, not
  // inside the fs plugin's root.
  const after = await gw.focused();
  expect(after.anchor, 'click on a rootless swatch must not descend').toBe(before.anchor);

  // Instead a notice must appear, attributed to this plugin by uuid, which is
  // ClickNotice's source key since labels can collide across connections, at
  // Info severity because it is a fixable configuration gap rather than a
  // failure, and naming the fix.
  const rootlessRow = pls.find((p) => p.label === 'rootless')!;
  const errs = await window.evaluate(() => (window as any).__gridwellTest.errors());
  const notice = errs.notices.find((n: any) => n.source === 'launcher:' + rootlessRow.uuid);
  expect(notice, 'a notice keyed by the plugin uuid is present').toBeTruthy();
  expect(notice.severity).toBe('info');
  expect(notice.message).toContain('no root configured');
});
