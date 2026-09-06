import { test, expect } from './fixtures';

// Crosses the plugin-classification seam: a plugin whose Info handshake succeeds
// but declares no root grid, such as an fs plugin with no config.root, must look
// and behave differently from an enterable one. Otherwise it renders as an
// ordinary swatch whose click silently does nothing.
//
// There are two non-enterable statuses, not three: every failure is "broken",
// whatever the reason, and only the reason text differs. A plugin that answered
// and declared no root is one of those failures — nothing to enter is not
// working — so it draws in the alarm tint and reports at Error severity, with
// the missing config named in the message for whoever is debugging. "Waiting"
// is the other status and means asked-not-answered, which only a connection row
// can be; conn-config.spec.ts covers it.
//
// The crashed-plugin case, where Info fails outright, is not covered here: a
// plugin that fails to spawn aborts the whole server by design, in
// internal/plugin's loader, so no real app boot reaches it. That case is
// covered at the buildPluginInfo and pluginInfo unit level in
// internal/server/plugininfo_test.go.
test.use({ extraPlugins: [{ kind: 'fs', name: 'noroot' }] });

test('a plugin with no root is inert and reports a notice instead of descending', async ({
  gw,
  window,
}) => {
  const pls = await gw.plugins();
  const noroot = pls.find((p) => p.label === 'noroot');
  expect(noroot, 'rootless fs plugin configured').toBeTruthy();
  expect(noroot!.rootGridID, 'it declared no root grid').toBe('');
  expect(noroot!.status, 'nothing to enter is a failure: broken, not waiting').toBe('broken');
  expect(noroot!.infoError, 'and it got there without any Info error').toBe('');

  // Boot skipped it and landed on home.
  const before = await gw.focused();
  expect(before.anchor, 'boots into the first rooted plugin, not the fs plugin').toBe(
    pls.find((p) => p.label === 'home')!.rootGridID,
  );

  // The + menu shows the swatch with its classification, the tint the render
  // path reads. Click it.
  await gw.openPalette();
  await gw.expandPlugins();
  const pal = await gw.palette();
  const swatch = pal.items.find((i) => i.isPlugin && i.label === 'noroot');
  expect(swatch, 'the plugin swatch is in the + menu').toBeTruthy();
  expect(swatch!.status, 'swatch carries the pluginhealth classification').toBe('broken');
  await window.mouse.click(swatch!.x + swatch!.w / 2, swatch!.y + swatch!.h / 2);
  await gw.waitIdle();

  // It must not have descended: the focused pane is still anchored at home, not
  // inside the fs plugin's root.
  const after = await gw.focused();
  expect(after.anchor, 'click on a non-enterable swatch must not descend').toBe(before.anchor);

  // Instead a notice must appear, attributed to this plugin by uuid, which is
  // ClickNotice's source key since labels can collide across connections, at
  // Error severity like every other broken row, and carrying the specific
  // reason so the fix is findable.
  const norootRow = pls.find((p) => p.label === 'noroot')!;
  const errs = await window.evaluate(() => (window as any).__gridwellTest.errors());
  const notice = errs.notices.find((n: any) => n.source === 'launcher:' + norootRow.uuid);
  expect(notice, 'a notice keyed by the plugin uuid is present').toBeTruthy();
  expect(notice.severity).toBe('error');
  expect(notice.message).toContain('no root configured');
});
