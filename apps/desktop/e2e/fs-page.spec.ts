import { test, expect } from './fixtures';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';

// The web-content door, desktop side: an fs image file is a url tile whose page
// the plugin serves, and descending it goes live as a native WebContentsView at
// the derived /content/<token>/<tile-id>/ address — the url-tile semantics, on a
// url tile. Ascending closes the view and persists nothing: the frozen face is
// the plugin's own thumbnail derivation, so the tile row stays byte-for-byte as
// it was.

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
  expect(cat.kind, 'an image file is a url tile').toBe('url');
  expect(cat.servesPage, 'whose page the plugin serves at the door').toBe(true);
  expect(cat.urlString ?? '', 'and which carries no address of its own').toBe('');
  const versionBefore = Number(cat.version ?? 0);

  // A url descent goes live, so a native view opens at the derived door
  // address: token, qualified tile id, and the trailing slash that relative
  // URLs inside the page depend on.
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
      { message: 'the descent opens the page live (issue #202, over the served page)', timeout: 15_000 },
    )
    .toMatch(new RegExp(`/content/[0-9a-f]{64}/${cat.id}/$`));

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
