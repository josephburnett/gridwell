import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// The parameterized-plugin flow (issue #251), crossed at the real seams with
// a REAL ssh plugin and an unreachable remote: dialing is lazy, so creating,
// listing, dedup-refusing, and tombstoning connections are all server-real
// without a live machine on the other end. What an unreachable remote CANNOT
// produce is a Ready entry (the chain is learned from the remote's first
// Info), so the adopt/descend half is pinned by the store, dispatch, and
// sshhost seam tests instead.

test.use({ extraPlugins: [{ kind: 'remote', name: 'connections' }] });

const HOST_FIELDS: Record<string, string> = {
  host: '127.0.0.1',
  user: 'joe',
  port: '1',
  key: '/nonexistent-key',
  known_hosts: '/nonexistent-kh',
};

async function fillConnectionForm(window: any): Promise<void> {
  for (const [name, value] of Object.entries(HOST_FIELDS)) {
    await window.fill(`#gw-inst-picker input[name=${name}]`, value);
  }
}

test('drop → picker: create, list as connecting, dedup by name, tombstone forever', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('local');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);

  const ssh = (await gw.plugins()).find((p) => p.kind === 'remote')!;
  expect(ssh.status, 'ssh classifies as parameterized after the flip').toBe('parameterized');
  expect(ssh.rootGridID, 'no root grid — no landing page').toBe('');
  expect(ssh.instanceGridID).toBe(`${ssh.uuid}/0`);

  // The menu drag drops an UNCONFIGURED plugin well: childless, marked with
  // the plugin's uuid, in the HOST grid (localdb).
  await gw.openPalette();
  await gw.dragPluginLink('remote', cx, cy);
  let g = await gw.getGrid(f.gridID);
  const well = tileAt(g, 'well', cx, cy)!;
  expect(well, 'the drop lands a well in the host grid').toBeTruthy();
  expect(well.childGridId ?? '', 'childless until an instance is adopted').toBe('');
  expect(well.configurePluginId, 'marked with the parameterizing plugin').toBe(ssh.uuid);

  // Descending it opens the picker — empty list, creation form.
  await gw.descendCell(cx, cy);
  await window.locator('#gw-inst-picker #gw-pick-new').waitFor({ timeout: 10_000 });

  // Create a connection. The host is unreachable, but creation is real:
  // the row + minted segment land in ssh's DB (the server oracle sees it).
  await window.fill('#gw-pick-name', 'gpu-box');
  await fillConnectionForm(window);
  await window.click('#gw-pick-create');
  await expect
    .poll(async () => ((await gw.getGrid(ssh.instanceGridID)).tiles ?? []).length, {
      timeout: 10_000,
    })
    .toBe(1);

  // The WAIT says WHY (errors must surface): the plugin's recorded dial
  // failure — here the unreadable key — reaches the picker while it is
  // still waiting, not a bare "connecting…" shrug.
  await expect(window.locator('#gw-pick-err')).toContainText('read key', { timeout: 10_000 });

  // Dismiss mid-wait. Things stay as you left them: the host well is still
  // unconfigured, byte-identical.
  await window.keyboard.press('Escape');
  await expect(window.locator('#gw-inst-picker')).toBeHidden();
  g = await gw.getGrid(f.gridID);
  expect(tileAt(g, 'well', cx, cy)!.childGridId ?? '').toBe('');

  // Re-descending lists the connection by name, honestly as connecting.
  await gw.descendCell(cx, cy);
  await window.locator('#gw-inst-picker #gw-pick-row-0').waitFor({ timeout: 10_000 });
  await expect(window.locator('#gw-pick-row-0')).toContainText('gpu-box');
  await expect(window.locator('#gw-pick-row-0')).toContainText('connecting');
  // The row carries the reason too — it's where you stare when a
  // connection won't come up.
  await expect(window.locator('#gw-pick-row-0')).toContainText('read key');

  // Identical details are a DUPLICATE of gpu-box — refused by name, never a
  // twin (one param-set, one minted segment).
  await fillConnectionForm(window);
  await window.click('#gw-pick-create');
  await expect(window.locator('#gw-pick-err')).toContainText('gpu-box');
  expect(((await gw.getGrid(ssh.instanceGridID)).tiles ?? []).length, 'no twin minted').toBe(1);

  // Delete is the tombstone gesture: two clicks, and the button says what
  // forever means before the second one.
  await window.click('#gw-pick-del-0');
  await expect(window.locator('#gw-pick-del-0')).toHaveText('forever?');
  await window.click('#gw-pick-del-0');
  await expect
    .poll(async () => ((await gw.getGrid(ssh.instanceGridID)).tiles ?? []).length, {
      timeout: 10_000,
    })
    .toBe(0);
});

test('the menu click opens the picker in place; Escape changes nothing', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('local');
  const before = await gw.focused();

  await gw.clickPluginSwatch('remote');
  await window.locator('#gw-inst-picker #gw-pick-body').waitFor({ timeout: 10_000 });
  await expect(window.locator('#gw-pick-body')).toContainText('no connections yet');

  await window.keyboard.press('Escape');
  await expect(window.locator('#gw-inst-picker')).toBeHidden();
  const after = await gw.focused();
  expect(after.gridID, 'a dismissed picker leaves the pane where it was').toBe(before.gridID);
  expect(after.anchor).toBe(before.anchor);
});
