import { test, expect } from './fixtures';

// Config-managed connections (v2 #269, reversing #199): server.yaml owns
// the list. The picker becomes PICK-ONLY — the declared label shows on
// the row (the yaml is the user speaking: it latches over the auto
// label), and no create form / rename / delete render, because the
// transport declares no creation schema in config mode (the declaration
// channel is the gate; those commits would only refuse).

test.use({
  extraPlugins: [{ kind: 'remote', name: 'connections' }],
  extraYaml: `connections:
    - name: fixedcon
      label: rtb
      addr: 127.0.0.1:1
`,
});

test('yaml connections: labeled row, pick-only picker', async ({ gw, window }) => {
  await gw.enterPlugin('local');
  await gw.clickPluginSwatch('remote');
  await window.locator('#gw-inst-picker #gw-pick-row-0').waitFor({ timeout: 10_000 });

  // The declared label, not the auto-label (the latch bug, 2026-08-23).
  await expect(window.locator('#gw-pick-row-0')).toContainText('rtb');
  await expect(window.locator('#gw-pick-row-0')).not.toContainText('@127.0.0.1');

  // Pick-only: no create form, no manage affordances.
  await expect(window.locator('#gw-pick-new')).toHaveCount(0);
  await expect(window.locator('#gw-pick-ren-0')).toHaveCount(0);
  await expect(window.locator('#gw-pick-del-0')).toHaveCount(0);
});

test('a connection presents as its own menu row', async ({ gw }) => {
  // v2 #269: the instance rows ride ListPlugins, so "rtb" sits in the
  // + menu top row like a plugin. This one is unreachable (127.0.0.1:1),
  // so it lists rootless/broken — present and labeled, inert until the
  // remote answers.
  await gw.enterPlugin('local');
  const rtb = (await gw.plugins()).find((p) => p.label === 'rtb');
  expect(rtb, 'the connection is a menu row of its own').toBeTruthy();
  expect(rtb!.uuid.includes('/'), 'a chained namespace identifies it').toBe(true);
});
