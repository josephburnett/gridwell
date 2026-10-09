import { test, expect } from './fixtures';
import { getGrid } from './oracle';

// A connection that will not answer can be switched off from its health
// notice until the node restarts (plugin.Registry.Disable). The seam runs from
// the button the strip draws (errsurface.Offer), through the verb, to the
// node's health event, which is what takes the notice down; the press itself
// clears nothing. Nobody answers at 127.0.0.1:1, so the connection is down
// from boot.

test.use({
  extraYaml: `connections:
    - name: awaycon
      label: away
      addr: 127.0.0.1:1
`,
});

async function notices(window: any): Promise<any[]> {
  return (await window.evaluate(() => (window as any).__gridwellTest.errors())).notices ?? [];
}

test('a source that will not answer is disabled from its notice and stays dark', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const away = (await gw.plugins()).find((p) => p.label === 'away')!;
  expect(away, 'the connection is declared').toBeTruthy();
  const source = 'plugin:' + away.uuid;
  const health = async () => (await notices(window)).find((n: any) => n.source === source);

  await expect
    .poll(async () => (await health())?.button, { message: 'the down notice offers Disable', timeout: 30_000 })
    .toBeTruthy();
  // The home's own notices, if any, are not this node's to switch off.
  for (const n of await notices(window)) {
    if (n.source !== source) expect(n.button, `${n.source} carries no button`).toBeFalsy();
  }

  const b = (await health()).button;
  await window.mouse.click(b.x + b.w / 2, b.y + b.h / 2);
  await expect
    .poll(async () => await health(), { message: 'the disabled event takes the notice down', timeout: 15_000 })
    .toBeUndefined();
  expect(
    (await notices(window)).filter((n: any) => n.source === 'rpc:DisableSource'),
    'the verb was accepted',
  ).toEqual([]);

  // Dark, not dead: the node answers a read through the name as unavailable.
  let refusal = '';
  try {
    await getGrid(gw.origin, away.uuid + '/far1/1');
  } catch (e) {
    refusal = String(e);
  }
  expect(refusal, 'a read through a disabled connection is refused unavailable').toContain('"code":"unavailable"');

  // The node holds it, not this page: a fresh page is told at once and raises
  // nothing.
  await window.reload();
  await window.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
  await gw.plugins();
  await gw.waitIdle();
  await window.waitForTimeout(2_000);
  expect(await health(), 'no notice comes back for a disabled source').toBeUndefined();
});
