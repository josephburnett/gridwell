import { test, expect } from './fixtures';

// Config-managed connections (v2 #269, reversing #199): server.yaml owns
// the list, and each connection presents as its OWN menu row — one icon
// per configured thing. The transport's own row (once the picker door)
// is gone entirely: with creation, rename, and delete all config-side,
// the connection dialog had no job left and was deleted (2026-08-23).

test.use({
  extraPlugins: [{ kind: 'remote', name: 'connections' }],
  extraYaml: `connections:
    - name: fixedcon
      label: rtb
      addr: 127.0.0.1:1
`,
});

test('one menu row per connection; the picker row is gone', async ({ gw }) => {
  await gw.enterPlugin('local');
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
