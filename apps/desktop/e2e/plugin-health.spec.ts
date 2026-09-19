import { test, expect } from './fixtures';

// A plugin whose Info succeeds and declares no grid of its own, an fs plugin
// with no config.root, is healthy: a plugin contributes menu entries and is not
// itself a place, so having nothing to enter is not a failure. The non-healthy
// statuses are "broken" and "waiting", which only a connection row can be in
// (conn-config.spec.ts). A plugin whose Info fails outright aborts the server
// at spawn, so no app boot reaches it; internal/server/plugininfo_test.go
// covers that shape.
test.use({ extraPlugins: [{ kind: 'fs', name: 'noroot' }] });

test('a plugin that declares no doorway is healthy and shows nothing', async ({ gw, window }) => {
  const pls = await gw.plugins();
  const noroot = pls.find((p) => p.label === 'noroot');
  expect(noroot, 'rootless fs plugin configured').toBeTruthy();
  expect(noroot!.rootGridID, 'it declared no grid of its own').toBe('');
  expect(noroot!.infoError, 'and it answered without any Info error').toBe('');
  expect(noroot!.status, 'a plugin is not a place, so it has no status at all').toBe('');

  // A node's home is where "/" means, and no plugin competes for it.
  const before = await gw.focused();
  expect(before.anchor, 'boots into the node home').toBe(
    pls.find((p) => p.label === 'home')!.rootGridID,
  );

  await gw.openPalette();
  await gw.expandPlugins();
  const pal = await gw.palette();
  const swatch = pal.items.find((i) => i.isPlugin && i.label === 'noroot');
  expect(swatch, 'a plugin with no doorway contributes no swatch').toBeFalsy();

  const errs = await window.evaluate(() => (window as any).__gridwellTest.errors());
  const notice = errs.notices.find((n: any) => n.source === 'doorway:' + noroot!.uuid);
  expect(notice, 'a healthy plugin reports nothing').toBeFalsy();
});
