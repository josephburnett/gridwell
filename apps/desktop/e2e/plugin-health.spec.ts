import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';
import { test, expect } from './fixtures';

// A plugin whose Info succeeds and declares no grid of its own, an fs plugin
// with no config.root, is healthy: a plugin contributes menu entries and is not
// itself a place, so having nothing to enter is not a failure. A plugin that
// cannot serve the source its config declares refuses Info with the reason, and
// is broken with that sentence until the source is fixed, without a restart.
test.describe('a plugin that declares no doorway', () => {
  test.use({ extraPlugins: [{ kind: 'fs', name: 'noroot' }] });

  test('is healthy and shows nothing', async ({ gw, window }) => {
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
});

// One path per worker process, created by the test mid-run: the typo'd root
// the user then fixes.
const missingRoot = path.join(os.tmpdir(), `gw-e2e-missing-root-${process.pid}`);

test.describe('a plugin that cannot serve its source', () => {
  test.use({ extraPlugins: [{ kind: 'fs', name: 'typo', config: { root: missingRoot } }] });
  test.beforeEach(() => fs.rmSync(missingRoot, { recursive: true, force: true }));
  test.afterEach(() => fs.rmSync(missingRoot, { recursive: true, force: true }));

  test('is broken with the reason, and comes back once fixed', async ({ gw, window }) => {
    const typo = async () => (await gw.plugins()).find((p) => p.label === 'typo')!;
    const broken = await typo();
    expect(broken, 'the misconfigured plugin is still listed').toBeTruthy();
    expect(broken.status, 'it is broken, not silently healthy').toBe('broken');
    expect(broken.infoError, "the row carries the plugin's own sentence").toBe(
      `root "${missingRoot}" does not exist`,
    );
    expect(broken.menuEntries, 'and none of its entries').toHaveLength(0);

    await gw.openPalette();
    await gw.expandPlugins();
    const pal = await gw.palette();
    expect(
      pal.items.find((i) => i.isPlugin && i.label === 'typo'),
      'a broken plugin has a swatch, which is where its reason is read',
    ).toBeTruthy();
    await window.keyboard.press('Escape');

    // The fix is on disk, and nothing is restarted: the node re-asks on its
    // fan-in's backoff and the client re-reads the plugin list on the up.
    fs.mkdirSync(missingRoot);
    await expect
      .poll(async () => (await typo()).status, {
        message: 'the fixed plugin comes back without a restart',
        timeout: 60_000,
      })
      .toBe('');
    const fixed = await typo();
    expect(fixed.infoError).toBe('');
    expect(fixed.menuEntries, 'its one collection is back').toHaveLength(1);
  });
});
