import { test, expect } from './fixtures';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { makeRunDir } from './homes';

// A body the source already knows it would refuse is never offered for
// editing: fs declares a file the process may not write read-only on its
// entry (Entry.read_only), so its descent shows the file with no editor and
// the bar says why, while a sibling it can write opens in the editor.

const ROOT = makeRunDir();
test.use({ extraPlugins: [{ kind: 'fs', name: 'code', config: { root: ROOT } }] });

async function descendInto(gw: any, name: string) {
  await gw.enterPlugin('code');
  const f = await gw.focused();
  const tile = ((await gw.getGrid(f.gridID)).tiles ?? []).find((t: any) => t.altText === name)!;
  expect(tile, `${name} listed`).toBeTruthy();
  await gw.descendCell(Number(tile.x ?? 0), Number(tile.y ?? 0));
  await expect.poll(async () => (await gw.focused()).textFocus).not.toBe('');
}

test('a file the process may not write opens read-only and says why', async ({ gw, window }) => {
  test.skip(process.getuid?.() === 0, 'root writes a 0444 file');
  const file = path.join(ROOT, 'locked.txt');
  if (fs.existsSync(file)) fs.chmodSync(file, 0o644);
  fs.writeFileSync(file, 'hands off\n');
  fs.chmodSync(file, 0o444);
  await descendInto(gw, 'locked.txt');

  await expect
    .poll(
      () =>
        window.evaluate(() => {
          const v = document.getElementById('gw-rendered-view');
          return v && v.style.display !== 'none' ? v.textContent ?? '' : '';
        }),
      { timeout: 10_000 },
    )
    .toContain('hands off');
  expect(await gw.textareaInfo(), 'a read-only body has no editor').toBeNull();
  await expect
    .poll(async () => (await gw.barName()).label, { timeout: 10_000 })
    .toMatch(/^locked\.txt — read-only: locked\.txt cannot be written: permission denied/);

  await window.keyboard.type('typed');
  await gw.waitIdle();
  expect(await gw.textareaInfo(), 'typing opened an editor').toBeNull();
  expect(fs.readFileSync(file, 'utf8')).toBe('hands off\n');
  const notices = await window.evaluate(() => (window as any).__gridwellTest.errors().notices as { source: string }[]);
  expect(
    notices.filter((n) => n.source.includes('WriteContent')),
    'no save was attempted and refused',
  ).toEqual([]);
});

test('its writable sibling opens in the editor', async ({ gw }) => {
  fs.writeFileSync(path.join(ROOT, 'open.txt'), 'type here\n');
  await descendInto(gw, 'open.txt');
  await expect.poll(async () => gw.textareaValue(), { timeout: 10_000 }).toBe('type here\n');
  expect((await gw.barName()).label).toBe('open.txt');
});
