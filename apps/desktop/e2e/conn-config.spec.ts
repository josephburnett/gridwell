import { test, expect } from './fixtures';

// Config-managed connections (v2 #269, reversing #199): server.yaml owns
// the list, and each connection presents as its OWN menu row — one icon
// per configured thing. The transport's own row (once the picker door)
// is gone entirely: with creation, rename, and delete all config-side,
// the connection dialog had no job left and was deleted (2026-08-23).

test.use({
  extraYaml: `connections:
    - name: fixedcon
      label: rtb
      addr: 127.0.0.1:1
`,
});

test('one menu row per connection; the picker row is gone', async ({ gw }) => {
  await gw.enterPlugin('home');
  const pls = await gw.plugins();

  // The declared label, not the auto-label (the latch bug, 2026-08-23);
  // unreachable here, so it lists rootless-inert until the remote answers.
  const rtb = pls.find((p) => p.label === 'rtb');
  expect(rtb, 'the connection is a menu row of its own').toBeTruthy();
  expect(rtb!.uuid.includes('/'), 'a chained namespace identifies it').toBe(true);

  // One icon per configured thing: the transport's own row is gone.
  expect(
    pls.find((p) => p.label === 'connections'),
    'the transport row is replaced by its instances',
  ).toBeUndefined();
});

test('clicking a pending connection says WHY, not nothing-to-descend-into', async ({
  gw,
  window,
}) => {
  // The 2026-08-23 laptop bug: the descent guard looked the plugin up by
  // LocalOf(id), which mangles a chained connection uuid, so the click
  // fell to the generic "nothing to descend into" instead of the
  // connection's own status.
  await gw.enterPlugin('home');
  const rtb = (await gw.plugins()).find((p) => p.label === 'rtb')!;
  await gw.clickPluginSwatch('rtb');
  // The notice keys by UUID — labels can collide across connections.
  await expect
    .poll(async () => {
      const errs = await window.evaluate(() => (window as any).__gridwellTest.errors());
      return (errs.notices ?? []).some((n: any) => n.source === 'launcher:' + rtb.uuid);
    })
    .toBe(true);
  const errs = await window.evaluate(() => (window as any).__gridwellTest.errors());
  expect(
    (errs.notices ?? []).some((n: any) => String(n.message).includes('nothing to descend into')),
    'the generic fallback must not fire for a known connection row',
  ).toBe(false);
});
