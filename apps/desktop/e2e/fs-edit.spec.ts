import { test, expect } from './fixtures';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { makeRunDir } from './homes';

// A text file in a plugin that declares writable is typed into and saved like
// a home document: the save writes the file, the plugin's own Watch echo of
// the write leaves the typed text alone, and bytes changed on disk under an
// unsaved edit refuse the save as a conflict and show the file as it is.

const ROOT = makeRunDir();
test.use({ extraPlugins: [{ kind: 'fs', name: 'code', config: { root: ROOT } }] });

const notices = (window: any) =>
  window.evaluate(() => (window as any).__gridwellTest.errors().notices as { source: string }[]);

async function openFile(gw: any, name: string, body: string) {
  fs.writeFileSync(path.join(ROOT, name), body);
  await gw.enterPlugin('code');
  const f = await gw.focused();
  const tile = ((await gw.getGrid(f.gridID)).tiles ?? []).find((t: any) => t.altText === name)!;
  expect(tile, `${name} listed`).toBeTruthy();
  await gw.descendCell(Number(tile.x ?? 0), Number(tile.y ?? 0));
  await expect.poll(async () => gw.textareaValue(), { timeout: 10_000 }).toBe(body);
}

test('typing into an fs file saves it to disk, and its echo leaves the typing alone', async ({
  gw,
  window,
}) => {
  const file = path.join(ROOT, 'edit.txt');
  await openFile(gw, 'edit.txt', 'line one\n');
  await window.keyboard.press('Control+End');
  await gw.typeText('typed here');
  await expect
    .poll(() => fs.readFileSync(file, 'utf8'), { timeout: 10_000 })
    .toBe('line one\ntyped here');

  // The plugin's Watch tells the write back as the entry's change; the body
  // is filed under the stamp it names, so the editor keeps its text and the
  // next keystrokes save on top of the first.
  await new Promise((r) => setTimeout(r, 2000));
  expect(await gw.textareaValue(), 'the echo of the save replaced the editor').toBe('line one\ntyped here');
  await gw.typeText(', and more');
  await expect
    .poll(() => fs.readFileSync(file, 'utf8'), { timeout: 10_000 })
    .toBe('line one\ntyped here, and more');
  expect(await gw.textareaValue()).toBe('line one\ntyped here, and more');
  expect(
    (await notices(window)).filter((n: { source: string }) => n.source.includes('WriteContent')),
    'a clean save says nothing',
  ).toEqual([]);
});

test('a file changed on disk under an unsaved edit refuses the save and shows the file', async ({
  gw,
  window,
}) => {
  const file = path.join(ROOT, 'clash.txt');
  await openFile(gw, 'clash.txt', 'mine\n');

  // The save cannot reach the node while the other writer changes the file,
  // so the edit is still unsaved, typed over bytes that are no longer there.
  await window.route('**/gridwell.v1.Gridwell/WriteContent', (r: any) => r.abort());
  await window.keyboard.press('Control+End');
  await gw.typeText('my edit');
  const later = new Date(Date.now() + 2000);
  fs.writeFileSync(file, 'theirs\n');
  fs.utimesSync(file, later, later);
  await new Promise((r) => setTimeout(r, 2000));
  expect(await gw.textareaValue(), 'the change on disk dropped the unsaved edit').toBe('mine\nmy edit');
  await window.unroute('**/gridwell.v1.Gridwell/WriteContent');

  // The next save claims the bytes the edit was typed over; the plugin refuses
  // it as a conflict, and the editor shows the file as it is.
  await gw.typeText('!');
  await expect.poll(async () => gw.textareaValue(), { timeout: 15_000 }).toBe('theirs\n');
  expect(fs.readFileSync(file, 'utf8'), "the other writer's bytes were overwritten").toBe('theirs\n');
  await expect
    .poll(async () => (await notices(window)).map((n: { source: string }) => n.source), { timeout: 10_000 })
    .toContain('conflict:WriteContent');
});
