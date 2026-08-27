import { test, expect } from './fixtures';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';

// The web-content door, browser side (2026-08-11): with no Electron bridge a
// serves_page tile cannot go live in place, so the descent shows the frozen
// face and the bar circle opens the DERIVED /content/ address in a new tab —
// the same degradation a url tile gets, because it is the same code path.
// The door itself must serve the image bytes sandboxed, with no cookie.

const PNG_1X1 = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==',
  'base64',
);

const picsDir = fs.mkdtempSync(path.join(os.tmpdir(), 'gridwell-web-page-'));
fs.writeFileSync(path.join(picsDir, 'cat.png'), PNG_1X1);

test.use({ extraPlugins: [{ kind: 'fs', name: 'pics', config: { root: picsDir } }] });

test('an fs image tile: the circle opens the /content/ page in a new tab', async ({
  gw,
  window,
}) => {
  await gw.enterPlugin('pics');
  const f = await gw.focused();
  const snap = await gw.getGrid(f.gridID);
  const cat = (snap.tiles ?? []).find((t) => t.altText === 'cat.png')!;
  expect(cat, 'the fs root grid lists cat.png').toBeTruthy();
  expect(cat.servesPage, 'an image file declares serves_page on the wire').toBe(true);

  // Descend: no bridge, so the tile stays frozen (DecideAutoLive's browser
  // arm) — no dead modal, no error; the pane presents the frozen face.
  await gw.descendCell(Number(cat.x ?? 0), Number(cat.y ?? 0));
  await expect.poll(async () => (await gw.focused()).textFocus).not.toBe('');

  // The circle is the open-in-new-tab affordance, exactly like a frozen url
  // tile on a browser host (owner decision 2026-08-09) — at the derived
  // door address, which serves the REAL image bytes.
  const pal = await gw.palette();
  const [popup] = await Promise.all([
    window.context().waitForEvent('page', { timeout: 10_000 }),
    window.mouse.click(pal.plusX, pal.plusY),
  ]);
  await popup.waitForURL(/\/content\//, { timeout: 10_000 });
  expect(popup.url()).toMatch(new RegExp(`/content/[0-9a-f]{64}/${cat.id}/$`));

  // The page the tab shows IS the file: same bytes, image content type,
  // sandboxed by the door (the token in the path is the whole credential —
  // this popup carries no auth cookie).
  const res = await popup.request.get(popup.url());
  expect(res.status()).toBe(200);
  expect(res.headers()['content-type']).toBe('image/png');
  expect(res.headers()['content-security-policy']).toBe('sandbox allow-scripts');
  expect(Buffer.from(await res.body()).equals(PNG_1X1)).toBe(true);
  await popup.close();
});
