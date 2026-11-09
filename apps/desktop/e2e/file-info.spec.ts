import { test, expect } from './fixtures';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { makeRunDir } from './homes';

// A file whose name renders as nothing shows its size and its extension's
// type on its preview with nothing read, its file information on a click, and
// its bytes in hex beside their UTF-8 under see more, paged through the
// content API. A source file still opens the editor.

const ROOT = makeRunDir();
test.use({ filesRoots: [{ root: ROOT, label: 'code' }] });

const shown = (window: any, id: string) =>
  window.evaluate((id: string) => {
    const el = document.getElementById(id) as HTMLElement | null;
    return el && el.offsetParent !== null ? el.textContent ?? '' : '';
  }, id);

test('a file that renders as nothing shows its file information and its bytes in hex', async ({
  gw,
  window,
}) => {
  // 70000 bytes is past one 64K page, so the end loads only on a scroll.
  const bytes = Buffer.alloc(70_000, 0x41);
  Buffer.from('\x7fELF caf\xc3\xa9', 'latin1').copy(bytes, 0);
  Buffer.from('THE END').copy(bytes, 70_000 - 7);
  fs.writeFileSync(path.join(ROOT, 'firmware.bin'), bytes);
  fs.chmodSync(path.join(ROOT, 'firmware.bin'), 0o640);
  fs.writeFileSync(path.join(ROOT, 'main.go'), 'package main\n');
  await gw.enterSource('code (files)');
  const f = await gw.focused();
  const tiles = (await gw.getGrid(f.gridID)).tiles ?? [];
  const bin = tiles.find((t) => t.altText === 'firmware.bin')!;
  expect((bin as { textPresentation?: string }).textPresentation, 'a .bin renders as nothing').toBe('info');

  // The preview reads the size off the stamp, so no body is asked for.
  const painted = async () =>
    (await window.evaluate(() => (window as any).__gridwellTest.paintedText()))[String(bin.id)] ?? '';
  await expect.poll(painted, { timeout: 10_000 }).toContain('69K  binary');

  await gw.descendCell(Number(bin.x ?? 0), Number(bin.y ?? 0));
  await expect.poll(async () => (await gw.focused()).textFocus).not.toBe('');
  const table = () => shown(window, 'gw-file-info-table');
  await expect.poll(table, { timeout: 10_000 }).toContain('ELF executable');
  expect(await table(), 'the size, as ls -h and in bytes').toContain('69K (70000 bytes)');
  expect(await table(), 'the mode as ls spells it').toContain('-rw-r----- (0640)');
  expect(await shown(window, 'gw-file-hex'), 'the hex view waits for see more').toBe('');
  expect(await shown(window, 'gw-text-editor'), 'no editor opens over it').toBe('');

  await window.click('#gw-file-see-more');
  const hex = () => shown(window, 'gw-file-hex');
  await expect.poll(hex, { timeout: 10_000 }).toContain('00000000  7f 45 4c 46 20 63 61 66  c3 a9 41 41 41 41 41 41  .ELF caféAAAAAA');
  expect(await shown(window, 'gw-file-hex-header')).toBe('offset 0 to 65536 of 70000 bytes');
  expect(await hex(), 'the second page waits for a scroll').not.toContain('THE END');

  await window.evaluate(() => {
    const v = document.getElementById('gw-file-info')!;
    v.scrollTop = v.scrollHeight;
  });
  await expect.poll(hex, { timeout: 10_000 }).toContain('THE END');
  expect(await shown(window, 'gw-file-hex-header')).toBe('offset 0 to 70000 of 70000 bytes');

  // A source file is still the editor, with the info button beside it.
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  const go = tiles.find((t) => t.altText === 'main.go')!;
  await gw.descendCell(Number(go.x ?? 0), Number(go.y ?? 0));
  await expect.poll(async () => gw.textareaValue(), { timeout: 10_000 }).toBe('package main\n');
  await window.click('#gw-info-toggle');
  await expect.poll(table, { timeout: 10_000 }).toContain('text/plain; charset=utf-8');
  expect(await shown(window, 'gw-text-editor'), 'the editor gives way to the file information').toBe('');
});
