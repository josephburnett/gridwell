import { test, expect } from './fixtures';
import { tileAt } from './oracle';

// Issue #209: drop first, prompt on first descent — the url half. Dragging
// the url swatch creates an ADDRESS-LESS tile immediately (no modal at
// drop); the first descent opens the url modal; submit writes the address
// as the tile's CONTENT (the store's url arm — versioned, validated, bumps)
// and descends straight into the live page. Cancel keeps the dropped tile.
// (The palette swatch CLICK — an ephemeral visit — still prompts up front:
// a "go to url now" gesture needs an address by nature.)

test('a url drops bare; the first descent prompts, writes the address, and goes live (#209)', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('local');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);

  // Drop: no modal, an address-less url tile at version 1.
  await gw.openPalette();
  await gw.dragCreate('url', cx, cy);
  const openModal = window.locator('#gw-url-modal.open');
  await expect(openModal, 'the drop never prompts (#209)').toHaveCount(0);
  let t = tileAt(await gw.getGrid(home.gridID), 'url', cx, cy)!;
  expect(t, 'the url tile landed at the drop cell').toBeTruthy();
  expect(t.urlString ?? '', 'dropped address-less').toBe('');

  // First descent prompts; cancel keeps the tile and stays on the grid.
  await gw.descendCell(cx, cy);
  await expect(openModal, 'first descent prompts for the address').toBeVisible();
  await window.locator('#gw-url-cancel').click();
  await expect(openModal).toHaveCount(0);
  expect((await gw.focused()).textFocus, 'cancel stays on the grid').toBe('');
  t = tileAt(await gw.getGrid(home.gridID), 'url', cx, cy)!;
  expect(t, 'cancel keeps the dropped tile').toBeTruthy();

  // Descend again, fill the address: it commits as content — a localdb row
  // is born at version 0 (protojson omits zero), so the write bumps it to 1
  // — and the pane descends into the live page.
  await gw.descendCell(cx, cy);
  await expect(openModal).toBeVisible();
  await window.fill('#gw-url-input', `${gw.origin}/wasm_exec.js?cfg=1`);
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await expect
    .poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 })
    .not.toBe('');
  await expect
    .poll(async () => {
      const row = tileAt(await gw.getGrid(home.gridID), 'url', cx, cy);
      return { url: String(row?.urlString ?? ''), version: String(row?.version ?? '') };
    })
    .toEqual({ url: `${gw.origin}/wasm_exec.js?cfg=1`, version: '1' });

  // Leave clean: ascend out of the url descent (retryable middle-click —
  // a click mid-animation is deliberately swallowed).
  const m = window.mouse;
  await expect
    .poll(
      async () => {
        const fp = await gw.focused();
        if (fp.textFocus !== '') {
          await m.move(fp.x + fp.w / 2, fp.y + fp.h / 2);
          await m.down({ button: 'middle' });
          await m.up({ button: 'middle' });
        }
        return fp.textFocus;
      },
      { timeout: 15_000, intervals: [700, 700, 1000, 1500] },
    )
    .toBe('');
  await gw.waitIdle();
});
