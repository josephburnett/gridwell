import { test, expect } from './fixtures';
import { Tile } from './oracle';

// The #198 creation-schema flow driven for real, against its first consumer
// (#199, the ssh connections plugin) — reshaped by #209 (drop first, prompt
// on descent): the drop commits IMMEDIATELY (a param-less connection well is
// a legal, inert state — dashed and childless), and the parameter form opens
// on the first DESCENT. Submit commits the params as the well's content;
// cancel keeps the dropped tile. The plugin validates authoritatively; a
// kind it refuses surfaces on the error strip instead of silently vanishing
// (charter §6).
//
// The ssh plugin runs in connections mode (no host config). The host we type
// is unreachable — deliberately: the well must exist, dashed and childless,
// while its remote is down (dialing is lazy; the child appears only when the
// remote answers). No test here ever waits on a network.

test.use({ extraPlugins: [{ kind: 'ssh', name: 'connections' }] });

test('a well drops bare on a schema grid; the first descent prompts and commits params (#209)', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('connections');
  const f = await gw.focused();

  // The drop commits immediately — no modal, a bare inert well at version 1.
  // (protojson renders int64 as a string and omits empty/zero fields.)
  await gw.openPalette();
  await gw.dragCreate('well', 2, 2);
  const modal = window.locator('#gw-schema-modal');
  await expect(modal, 'the drop never prompts (#209)').toBeHidden();
  await expect
    .poll(async () => {
      const snap = await gw.getGrid(f.gridID);
      const t = (snap.tiles ?? []).find((x: Tile) => x.kind === 'well');
      return t ? { version: String(t.version ?? '1'), ref: t.reference === true, child: t.childGridId ?? '' } : null;
    })
    .toEqual({ version: '1', ref: true, child: '' });

  // Descending into the unconfigured well opens the parameter form.
  await gw.descendCell(2, 2);
  await expect(modal, 'first descent prompts for the connection params').toBeVisible();

  // Escape cancels the CONFIGURE, not the tile: the dropped well stays.
  await window.keyboard.press('Escape');
  await expect(modal).toBeHidden();
  const g = await gw.getGrid(f.gridID);
  expect((g.tiles ?? []).length, 'cancel keeps the dropped tile').toBe(1);

  // Descend again and fill the form. host+user are the schema's required
  // fields; everything else defaults host-side.
  await gw.descendCell(2, 2);
  await expect(modal).toBeVisible();
  await modal.locator('input[name="host"]').fill('unreachable.test');
  await modal.locator('input[name="user"]').fill('joe');
  await modal.locator('button[type="submit"]').click();
  await expect(modal).toBeHidden();

  // The params land as the well's CONTENT (a versioned write — create is 1,
  // the params commit bumps to 2) and the auto label appears.
  await expect
    .poll(
      async () => {
        const snap = await gw.getGrid(f.gridID);
        const t = (snap.tiles ?? []).find((x: Tile) => x.kind === 'well');
        return t
          ? {
              version: String(t.version ?? ''),
              alt: t.altText,
              ref: t.reference === true,
              child: t.childGridId ?? '',
            }
          : null;
      },
      { timeout: 15_000 },
    )
    .toEqual({ version: '2', alt: 'joe@unreachable.test', ref: true, child: '' });
});

test('a kind the plugin refuses surfaces instead of vanishing', async ({ gw, window }) => {
  await gw.enterPlugin('connections');
  const f = await gw.focused();

  // The connection grid takes wells only; a markdown drop must be refused
  // LOUDLY — the optimistic tile reconciles away and the refusal reaches the
  // error strip, never a silent disappearance.
  await gw.openPalette();
  await gw.dragCreate('markdown', 2, 2);
  await expect
    .poll(async () => {
      const e = (await window.evaluate(() => (window as any).__gridwellTest.errors())) as {
        notices: { message: string }[];
      };
      return (e.notices ?? []).some((n) => /connection|well/i.test(n.message));
    })
    .toBe(true);
  const g = await gw.getGrid(f.gridID);
  expect((g.tiles ?? []).length, 'the refused create leaves no tile').toBe(0);
});
