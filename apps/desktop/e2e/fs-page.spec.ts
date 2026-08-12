import { test, expect } from './fixtures';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';

// The web-content door, desktop side (2026-08-11): an fs image tile declares
// serves_page, and DESCENDING it goes live — a native WebContentsView at the
// derived /content/<token>/<tile-id>/ address — with exactly the url-tile
// semantics (#202: descending is the engagement gesture). Ascending closes
// the view and persists NOTHING: the frozen face is the plugin's own
// thumbnail derivation, so the tile row stays byte-for-byte as it was.

// A real 1x1 PNG so the fs plugin classifies and serves an actual image.
const PNG_1X1 = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==',
  'base64',
);

const picsDir = fs.mkdtempSync(path.join(os.tmpdir(), 'gridwell-fs-page-'));
fs.writeFileSync(path.join(picsDir, 'cat.png'), PNG_1X1);

test.use({ extraPlugins: [{ kind: 'fs', name: 'pics', config: { root: picsDir } }] });

test('descending an fs image opens it live through the /content/ door', async ({
  electronApp,
  gw,
}) => {
  await gw.enterPlugin('pics');
  const f = await gw.focused();
  const snap = await gw.getGrid(f.gridID);
  const cat = (snap.tiles ?? []).find((t) => t.altText === 'cat.png')!;
  expect(cat, 'the fs root grid lists cat.png').toBeTruthy();
  expect(cat.servesPage, 'an image file declares serves_page on the wire').toBe(true);
  const versionBefore = Number(cat.version ?? 0);

  // Descend: the one auto-live owner gives serves_page the url verdict, so
  // a native view opens at the DERIVED door address — token + qualified
  // tile id + the load-bearing trailing slash.
  await gw.descendCell(Number(cat.x ?? 0), Number(cat.y ?? 0));
  await expect
    .poll(
      () =>
        electronApp.evaluate(({ webContents }) =>
          webContents
            .getAllWebContents()
            .map((w) => w.getURL())
            .find((u) => u.includes('/content/')),
        ),
      { message: 'the descent opens the page live (issue #202 applied to serves_page)', timeout: 15_000 },
    )
    .toMatch(new RegExp(`/content/[0-9a-f]{64}/${cat.id}/$`));

  // Ascend: the view closes, and NOTHING was persisted — no SetURLState,
  // no version bump; the row is byte-identical (the guiding rule).
  await gw.ascendViaCrumb();
  await expect
    .poll(
      () =>
        electronApp.evaluate(({ webContents }) =>
          webContents.getAllWebContents().some((w) => w.getURL().includes('/content/')),
        ),
      { timeout: 15_000 },
    )
    .toBe(false);
  const after = (await gw.getGrid(f.gridID)).tiles!.find((t) => t.altText === 'cat.png')!;
  expect(Number(after.version ?? 0), 'a page descent persists nothing').toBe(versionBefore);
  expect(after.previewBlobId ?? 0, 'no preview blob is minted for a page tile').toBe(cat.previewBlobId ?? 0);
});
