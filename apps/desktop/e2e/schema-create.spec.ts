import { test, expect } from './fixtures';
import { Tile } from './oracle';

// The #198 creation-schema flow driven for real, against its first consumer
// (#199, the ssh connections plugin): a grid whose plugin declares a create
// schema turns the well-drop gesture into a parameter form. Submit commits
// the metadata create THEN the params as the well's content; cancel commits
// nothing. The plugin validates authoritatively; a kind it refuses surfaces
// on the error strip instead of silently vanishing (charter §6).
//
// The ssh plugin runs in connections mode (no host config). The host we type
// is unreachable — deliberately: the well must exist, dashed and childless,
// while its remote is down (dialing is lazy; the child appears only when the
// remote answers). No test here ever waits on a network.

test.use({ extraPlugins: [{ kind: 'ssh', name: 'connections' }] });

test('a schema grid turns well-drop into a form; submit commits params as content', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('connections');
  const f = await gw.focused();

  // The drop gesture opens the form instead of committing.
  await gw.openPalette();
  await gw.dragCreate('well', 2, 2);
  const modal = window.locator('#gw-schema-modal');
  await expect(modal, 'the creation form opens for a schema-declaring grid').toBeVisible();

  // Escape cancels: nothing was created.
  await window.keyboard.press('Escape');
  await expect(modal).toBeHidden();
  let g = await gw.getGrid(f.gridID);
  expect((g.tiles ?? []).length, 'cancel commits nothing').toBe(0);

  // Again, this time filling the form. host+user are the schema's required
  // fields; everything else defaults host-side.
  await gw.openPalette();
  await gw.dragCreate('well', 2, 2);
  await expect(modal).toBeVisible();
  await modal.locator('input[name="host"]').fill('unreachable.test');
  await modal.locator('input[name="user"]').fill('joe');
  await modal.locator('button[type="submit"]').click();
  await expect(modal).toBeHidden();

  // The well lands; the params follow as its CONTENT (a versioned write —
  // create is 1, the params commit bumps to 2) and the auto label appears.
  // (protojson renders int64 as a string and omits empty fields.)
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
