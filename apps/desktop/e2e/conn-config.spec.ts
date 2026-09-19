import { test, expect } from './fixtures';

// Connections are config: server.yaml owns the list, and each connection
// presents as its own menu row, one icon per configured thing. The transport
// has no row of its own, and there is no connection dialog: creation, rename,
// and delete all happen in the config.

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

  // The label comes from the config. The remote is unreachable here, so the
  // row reads as waiting until the dial is attempted and broken once it has
  // failed; which of the two it is at this instant is a race, so this test
  // pins only the row's presence.
  const rtb = pls.find((p) => p.label === 'rtb');
  expect(rtb, 'the connection is a menu row of its own').toBeTruthy();
  expect(rtb!.uuid.includes('/'), 'a chained namespace identifies it').toBe(true);

  expect(
    pls.find((p) => p.label === 'connections'),
    'the transport row is replaced by its instances',
  ).toBeUndefined();
});

test('clicking a pending connection says WHY, not nothing-to-descend-into', async ({
  gw,
  window,
}) => {
  // The descent guard looks the row up by its full id. Resolving through
  // rpc.LocalOf mangles a chained connection uuid, and the click then falls to
  // the generic "nothing to descend into" instead of the connection's status.
  await gw.enterPlugin('home');
  const rtb = (await gw.plugins()).find((p) => p.label === 'rtb')!;
  await gw.clickPluginSwatch('rtb');
  // The notice keys by uuid, since labels can collide across connections.
  await expect
    .poll(async () => {
      const errs = await window.evaluate(() => (window as any).__gridwellTest.errors());
      return (errs.notices ?? []).some((n: any) => n.source === 'doorway:' + rtb.uuid);
    })
    .toBe(true);
  const errs = await window.evaluate(() => (window as any).__gridwellTest.errors());
  expect(
    (errs.notices ?? []).some((n: any) => String(n.message).includes('nothing to descend into')),
    'the generic fallback must not fire for a known connection row',
  ).toBe(false);
});
