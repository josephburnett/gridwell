import { test, expect } from './fixtures';

// Config-managed connections (v2 #269, reversing #199): server.yaml owns
// the list, and each connection presents as its OWN menu row — one icon
// per configured thing. The transport's parameterized row (the picker
// door) disappears entirely: with creation, rename, and delete all
// config-side, the connection dialog has no job left. (Legacy homes —
// no connections: key — keep the picker flow; inst-picker.spec covers
// it.)

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

  // One icon per configured thing: no parameterized "connections" row.
  expect(
    pls.find((p) => p.status === 'parameterized'),
    'the picker row is replaced by its instances',
  ).toBeUndefined();
});
