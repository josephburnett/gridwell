import * as fs from 'node:fs';
import * as path from 'node:path';
import { spawnSync } from 'node:child_process';
import { test, expect } from './fixtures';
import { homeEnv } from './homes';
import { shellSessionAlive, tileAt } from './oracle';

// A cloned shell is its own tile naming its source's tmux session
// (rpc.ShellSession). Any number of tiles may name one session and at most one
// is live: going live on the clone closes the source's terminal, which keeps
// its own face, and attaches the same session, so what was typed in one shows
// in the other, tmux sees one attached client sized to the clone's pane, and
// one exit leaves every tile on the session dead. This drives what no unit
// test can: the clone gesture, the stored session, the client's takeover by
// session, the /shell door and the real tmux behind it.

// The node's private tmux server (gridwell-<id>, internal/node), asked the
// way the node asks it: through the socket dir the home's environment names.
function tmuxClients(home: string): string[] {
  const id = /^id:\s*"?([^"\s]+)"?/m.exec(fs.readFileSync(path.join(home, 'server.yaml'), 'utf-8'))?.[1];
  const r = spawnSync('tmux', ['-L', `gridwell-${id}`, 'list-clients', '-F', '#{client_width}x#{client_height}'], {
    env: { ...process.env, ...homeEnv(home) },
    encoding: 'utf-8',
  });
  return r.status === 0 ? r.stdout.split('\n').filter((l) => l.trim() !== '') : [];
}

test('a clone of a live shell attaches its session: one client, one exit for both', async ({ gw, home, window }) => {
  const origin = new URL(window.url()).origin;
  const shellViews = (): Promise<Record<string, string>> =>
    window.evaluate(() => (window as any).__gridwellTest.shellViews());
  const shellText = (): Promise<string> => window.evaluate(() => (window as any).__gridwellTest.shellText());
  const buffer = (): Promise<{ cols: number; rows: number } | null> =>
    window.evaluate(() => (window as any).__gridwellTest.shellBuffer());
  const slot = () => window.evaluate(() => (window as any).__gridwellTest.barSlot());

  await gw.enterPlugin('home');
  const f = await gw.focused();
  const grid = f.gridID;
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  const shellAt = async (x: number, y: number) => tileAt(await gw.getGrid(grid), 'shell', x, y);

  // Split first: splitting a descended pane would ascend it.
  await gw.splitFocusedPaneVertical();
  const [left, right] = (await gw.panes()).slice().sort((a: any, b: any) => a.x - b.x);
  const pane = async (id: string) => (await gw.panes()).find((p: any) => p.id === id)!;

  await gw.focusPane(left);
  await gw.openPalette();
  await gw.dragCreate('shell', cx, cy);
  await gw.descendCell(cx, cy);
  await gw.shellAttached();
  const marker = `gw-shared-${Math.random().toString(36).slice(2, 8)}`;
  await window.keyboard.type(`printf '%s\\n' ${marker}`);
  await window.keyboard.press('Enter');
  const markerRow = async () => (await shellText()).split('\n').some((l) => l.trim() === marker);
  await expect.poll(markerRow, { timeout: 10_000 }).toBe(true);
  const source = (await shellAt(cx, cy))!;

  // Right-drag the live shell in the other pane: the copy names its session.
  await gw.focusPane(await pane(right.id));
  await gw.cloneTileCell(cx, cy, cx + 2, cy);
  const clone = (await shellAt(cx + 2, cy))!;
  expect(clone, 'the clone landed').toBeTruthy();
  expect(clone.id, 'the clone is its own tile').not.toBe(source.id);
  expect(clone.shellSession, 'the clone names its source session').toBe(source.id);

  // Going live on the clone takes the session over.
  await gw.descendCell(cx + 2, cy);
  await expect
    .poll(shellViews, { message: 'the clone holds the one live terminal', timeout: 15_000 })
    .toEqual({ [right.id]: clone.id });
  await gw.shellAttached();
  expect(await markerRow(), 'the clone shows what was typed in its source').toBe(true);

  // The source paused on a face of its own.
  await expect
    .poll(async () => Number((await shellAt(cx, cy))?.previewBlobId ?? 0), { timeout: 10_000 })
    .toBeGreaterThan(0);

  // tmux has one client, sized to the clone's terminal.
  await expect.poll(() => tmuxClients(home).length, { timeout: 10_000 }).toBe(1);
  await expect
    .poll(
      async () => {
        const b = await buffer();
        return b ? `${b.cols}x${b.rows}` === tmuxClients(home)[0] : false;
      },
      { message: 'the attached client is the clone pane size', timeout: 10_000 },
    )
    .toBe(true);

  // The source's refresh is on offer while the session lives.
  await gw.focusPane(await pane(left.id));
  await expect.poll(slot, { timeout: 15_000 }).toBe('golive');

  // One exit and every tile on the session is dead.
  await gw.focusPane(await pane(right.id));
  await window.keyboard.type('exit');
  await window.keyboard.press('Enter');
  await expect.poll(shellViews, { timeout: 15_000 }).toEqual({});
  await expect.poll(() => shellSessionAlive(origin, source.id), { timeout: 10_000 }).toBe(false);
  expect(await shellSessionAlive(origin, clone.id), 'the clone shares the death').toBe(false);
  await gw.focusPane(await pane(left.id));
  await expect.poll(slot, { message: 'the source hides its refresh', timeout: 15_000 }).not.toBe('golive');
  expect(tmuxClients(home)).toEqual([]);
});

test('a clone of a never-opened shell starts the session its source then joins', async ({ gw, window }) => {
  const shellText = (): Promise<string> => window.evaluate(() => (window as any).__gridwellTest.shellText());

  await gw.enterPlugin('home');
  const f = await gw.focused();
  const grid = f.gridID;
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  const shellAt = async (x: number, y: number) => tileAt(await gw.getGrid(grid), 'shell', x, y);

  await gw.openPalette();
  await gw.dragCreate('shell', cx, cy);
  await gw.cloneTileCell(cx, cy, cx + 2, cy);
  const source = (await shellAt(cx, cy))!;
  const clone = (await shellAt(cx + 2, cy))!;
  expect(clone.shellSession, 'the clone names the session not yet started').toBe(source.id);

  // The clone opens first and starts the session.
  await gw.descendCell(cx + 2, cy);
  await gw.shellAttached();
  const marker = `gw-first-${Math.random().toString(36).slice(2, 8)}`;
  await window.keyboard.type(`printf '%s\\n' ${marker}`);
  await window.keyboard.press('Enter');
  const markerRow = async () => (await shellText()).split('\n').some((l) => l.trim() === marker);
  await expect.poll(markerRow, { timeout: 10_000 }).toBe(true);
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 10_000 }).toBe('');

  // The source joins the same session rather than starting its own.
  await gw.descendCell(cx, cy);
  await gw.shellAttached();
  expect(await markerRow(), 'the source attached the session its clone started').toBe(true);

  // End the session so tmux never hangs the harness close.
  await window.keyboard.type('exit');
  await window.keyboard.press('Enter');
  await expect.poll(shellText, { timeout: 15_000 }).toBe('');
});
