import * as fs from 'node:fs';
import * as path from 'node:path';
import { spawnSync } from 'node:child_process';
import { test, expect } from './fixtures';
import { homeEnv, makeRunDir } from './homes';
import { tileAt } from './oracle';

// A drop onto a shell pastes (CLAUDE.md, 2026-11-08): a file dropped on a
// live shell types its absolute path, so `cat `, the drop and Enter show the
// file; a home tile dropped on a shell tile on the grid, its tmux running
// with no terminal attached, types its reference, the window's origin and
// the tile's place.

const ROOT = makeRunDir();
const MARKER = `pasted-${Math.random().toString(36).slice(2, 8)}`;
fs.writeFileSync(path.join(ROOT, 'notes.txt'), `${MARKER}\n`);
test.use({ filesRoots: [{ root: ROOT, label: 'docs' }] });

// What the node's private tmux shows of a shell, asked the way the node asks.
function paneText(home: string, key: string): string {
  const id = /^id:\s*"?([^"\s]+)"?/m.exec(fs.readFileSync(path.join(home, 'server.yaml'), 'utf-8'))?.[1];
  const name = 'gridwell-' + Buffer.from(key).toString('base64url');
  const r = spawnSync('tmux', ['-L', `gridwell-${id}`, 'capture-pane', '-p', '-J', '-t', name], {
    env: { ...process.env, ...homeEnv(home) },
    encoding: 'utf-8',
  });
  return r.status === 0 ? r.stdout : '';
}

test('a file dropped on a shell pastes its path; a tile, its reference', async ({ gw, home, window }) => {
  const origin = new URL(window.url()).origin;
  const shellText = (): Promise<string> => window.evaluate(() => (window as any).__gridwellTest.shellText());

  await gw.sources();
  await gw.splitFocusedPaneVertical();
  const bId = (await gw.focused()).id;
  const a0 = (await gw.panes()).find((p: any) => p.id !== bId)!;
  await gw.clickScreen(a0.x + 20, a0.y + 20);
  await gw.enterSource('home');
  const a = (await gw.panes()).find((p: any) => p.id === a0.id)!;
  const cx = Math.round(a.cx);
  const cy = Math.round(a.cy) - 1;

  // A shell and a text beside it, made as the user makes them.
  await gw.openMenu();
  await gw.dragCreate('shell', cx, cy);
  await gw.openMenu();
  await gw.dragCreate('markdown', cx + 1, cy);
  const shell = tileAt(await gw.getGrid(a.gridID), 'shell', cx, cy)!;
  const text = tileAt(await gw.getGrid(a.gridID), 'text', cx + 1, cy)!;
  expect(shell && text, 'the shell and the text were made').toBeTruthy();

  const b0 = (await gw.panes()).find((p: any) => p.id === bId)!;
  await gw.clickScreen(b0.x + 20, b0.y + 20);
  await gw.enterSource('docs (files)');
  const b = (await gw.panes()).find((p: any) => p.id === bId)!;
  const notes = ((await gw.getGrid(b.gridID)).tiles ?? []).find((t: any) => t.altText === 'notes.txt')!;
  expect(notes, 'the file is listed').toBeTruthy();

  // Go live on the shell and type `cat `.
  await gw.focusPane((await gw.panes()).find((p: any) => p.id === a.id)!);
  await gw.descendCell(cx, cy);
  await gw.shellAttached();
  await window.keyboard.type('cat ');

  // Drop the file on the live shell, then Enter in the shell.
  const live = (await gw.panes()).find((p: any) => p.id === a.id)!;
  const from = await gw.cellCenter(b.id, Number(notes.x ?? 0), Number(notes.y ?? 0));
  const m = window.mouse;
  await m.move(from.x, from.y);
  await m.down({ button: 'left' });
  await m.move(from.x + 8, from.y + 8);
  await m.move(live.x + live.w / 2, live.y + live.h / 2, { steps: 10 });
  await m.up({ button: 'left' });
  await gw.waitIdle();
  // The press focused the file's pane; the shell's terminal is read focused.
  await gw.clickScreen(live.x + live.w / 2, live.y + live.h / 2);
  const pathTyped = async () => (await shellText()).includes(`cat ${path.join(ROOT, 'notes.txt')}`);
  await expect.poll(pathTyped, { message: 'the path was pasted after cat', timeout: 10_000 }).toBe(true);
  const after = ((await gw.getGrid(b.gridID)).tiles ?? []).find((t: any) => t.id === notes.id)!;
  expect([after.x, after.y], 'the file did not move').toEqual([notes.x, notes.y]);
  await window.keyboard.press('Enter');
  const shown = async () => (await shellText()).split('\n').some((l) => l.trim() === MARKER);
  await expect.poll(shown, { message: 'cat showed the file', timeout: 10_000 }).toBe(true);

  // Leave the shell, its tmux running, and drop the text tile on its tile.
  await window.keyboard.type('echo ');
  await gw.ascendViaCrumb();
  await gw.dragTileCell(cx + 1, cy, cx, cy);
  const key = shell.id.split('/').pop()!;
  const ref = `${origin}/${a.gridID}/${text.id.split('/').pop()}`;
  await expect
    .poll(() => paneText(home, key).includes(`echo ${ref}`), { message: `the reference ${ref} was pasted`, timeout: 10_000 })
    .toBe(true);
  expect(tileAt(await gw.getGrid(a.gridID), 'text', cx + 1, cy)?.id, 'the text did not move').toBe(text.id);
});
