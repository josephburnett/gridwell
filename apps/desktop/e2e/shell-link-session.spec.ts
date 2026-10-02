import * as fs from 'node:fs';
import * as path from 'node:path';
import { spawnSync } from 'node:child_process';
import { test, expect } from './fixtures';
import { homeEnv } from './homes';
import { tileAt } from './oracle';

// A link to a shell attaches the session its target names (shellconn.SessionKey),
// so a link to a clone shares the clone's source's session like the clone does:
// going live on the link takes the session over from the source, which pauses on
// a face of its own, and going live on the source takes it back. The node keys
// its one attached client by session, so a client that keyed the link by its
// target's id would keep the source's terminal open beside it until the node
// evicted that socket, and the source would end on the evicted socket's exit
// rather than a takeover's close.

function tmuxClients(home: string): string[] {
  const id = /^id:\s*"?([^"\s]+)"?/m.exec(fs.readFileSync(path.join(home, 'server.yaml'), 'utf-8'))?.[1];
  const r = spawnSync('tmux', ['-L', `gridwell-${id}`, 'list-clients', '-F', '#{client_width}x#{client_height}'], {
    env: { ...process.env, ...homeEnv(home) },
    encoding: 'utf-8',
  });
  return r.status === 0 ? r.stdout.split('\n').filter((l) => l.trim() !== '') : [];
}

test('a link to a clone takes over its source session, and the source takes it back', async ({ gw, home, window }) => {
  // An evicted socket is the one way a terminal ends with an exit while its
  // session lives; a takeover closes instead.
  const exits: string[] = [];
  window.on('console', (msg) => {
    if (msg.text().startsWith('[shellstream] exit ')) exits.push(msg.text());
  });
  const shellViews = (): Promise<Record<string, string>> =>
    window.evaluate(() => (window as any).__gridwellTest.shellViews());
  const shellText = (): Promise<string> => window.evaluate(() => (window as any).__gridwellTest.shellText());
  const slot = () => window.evaluate(() => (window as any).__gridwellTest.barSlot());
  const clickSlot = async () => {
    const pal = await gw.palette();
    await gw.clickScreen(pal.plusX, pal.plusY);
  };

  await gw.enterPlugin('home');
  const f = await gw.focused();
  const grid = f.gridID;
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  const shellAt = async (x: number, y: number) => tileAt(await gw.getGrid(grid), 'shell', x, y);

  await gw.splitFocusedPaneVertical();
  const [left, right] = (await gw.panes()).slice().sort((a: any, b: any) => a.x - b.x);
  const pane = async (id: string) => (await gw.panes()).find((p: any) => p.id === id)!;

  // The source, live in the left pane.
  await gw.focusPane(left);
  await gw.openPalette();
  await gw.dragCreate('shell', cx, cy);
  await gw.descendCell(cx, cy);
  await gw.shellAttached();
  const marker = `gw-linked-${Math.random().toString(36).slice(2, 8)}`;
  await window.keyboard.type(`printf '%s\\n' ${marker}`);
  await window.keyboard.press('Enter');
  const markerRow = async () => (await shellText()).split('\n').some((l) => l.trim() === marker);
  await expect.poll(markerRow, { timeout: 10_000 }).toBe(true);
  const source = (await shellAt(cx, cy))!;

  // A clone of it, and a link to the clone, from the right pane.
  await gw.focusPane(await pane(right.id));
  await gw.cloneTileCell(cx, cy, cx + 2, cy);
  const clone = (await shellAt(cx + 2, cy))!;
  expect(clone.shellSession, 'the clone names its source session').toBe(source.id);
  await gw.linkTileCell(cx + 2, cy, cx + 4, cy);
  const link = (await shellAt(cx + 4, cy))!;
  expect(link.linkTargetId, 'the link names the clone').toBe(clone.id);

  // Going live on the link takes the session over.
  await gw.descendCell(cx + 4, cy);
  await expect
    .poll(shellViews, { message: 'the link holds the one live terminal', timeout: 15_000 })
    .toEqual({ [right.id]: clone.id });
  await gw.shellAttached();
  expect(await markerRow(), 'the link shows what was typed in the source').toBe(true);
  await expect
    .poll(async () => Number((await shellAt(cx, cy))?.previewBlobId ?? 0), {
      message: 'the source paused on a face of its own',
      timeout: 10_000,
    })
    .toBeGreaterThan(0);
  await expect.poll(() => tmuxClients(home).length, { timeout: 10_000 }).toBe(1);
  expect(await shellViews(), 'one holder, never the source beside it').toEqual({ [right.id]: clone.id });
  expect(exits, 'the source closed by takeover, not by an evicted socket').toEqual([]);

  // Going live on the source takes it back.
  await gw.focusPane(await pane(left.id));
  await expect.poll(slot, { timeout: 15_000 }).toBe('golive');
  await clickSlot();
  await expect
    .poll(shellViews, { message: 'the source holds the one live terminal', timeout: 15_000 })
    .toEqual({ [left.id]: source.id });
  await gw.shellAttached();
  await expect.poll(() => tmuxClients(home).length, { timeout: 10_000 }).toBe(1);
  expect(exits, 'the link closed by takeover, not by an evicted socket').toEqual([]);

  // And the link's own go-live takes it again, by the bar this time.
  await gw.focusPane(await pane(right.id));
  await expect.poll(slot, { timeout: 15_000 }).toBe('golive');
  await clickSlot();
  await expect
    .poll(shellViews, { message: 'the link holds the one live terminal again', timeout: 15_000 })
    .toEqual({ [right.id]: clone.id });
  await gw.shellAttached();
  expect(await shellViews()).toEqual({ [right.id]: clone.id });
  expect(exits, 'the source closed by takeover again').toEqual([]);

  // End the session so tmux never hangs the harness close.
  await window.keyboard.type('exit');
  await window.keyboard.press('Enter');
  await expect.poll(shellViews, { timeout: 15_000 }).toEqual({});
});
