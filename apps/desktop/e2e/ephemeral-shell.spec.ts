import * as fs from 'node:fs';
import * as path from 'node:path';
import { spawnSync } from 'node:child_process';
import { test, expect } from './fixtures';
import { homeEnv } from './homes';
import { trashed } from './oracle';

// Whether the node's private tmux runs the session a shell's key names,
// asked of tmux itself, so a destroyed row's answer is not the node's.
function tmuxRunning(home: string, key: string): boolean {
  const id = /^id:\s*"?([^"\s]+)"?/m.exec(fs.readFileSync(path.join(home, 'server.yaml'), 'utf-8'))?.[1];
  const name = 'gridwell-' + Buffer.from(key).toString('base64url');
  return spawnSync('tmux', ['-L', `gridwell-${id}`, 'has-session', '-t', `=${name}`], {
    env: { ...process.env, ...homeEnv(home) },
    stdio: 'ignore',
  }).status === 0;
}

// Clicking the shell swatch, rather than dragging it, opens an ephemeral shell:
// created in the off-grid scratch grid, descended into, PTY spawned. Ascending
// sends the tile to the trash with its tmux running, so it reopens from there
// as it was left, and destroying it from the trash ends the tmux. Nothing
// lands on the home grid and no error surfaces.

test('clicking the shell swatch opens an ephemeral shell; ascent sends it to the trash, its tmux running', async ({
  window,
  gw,
  home: nodeDir,
}) => {
  const tileCount = (g: { tiles?: unknown[] }) => (g.tiles ?? []).length;

  await gw.enterSource('home');
  const scratchGridID = await gw.scratchGridID('home');
  const home = await gw.focused();
  const homeBefore = await gw.getGrid(home.gridID);

  // Clicking the swatch descends into a live shell.
  await gw.clickMenuSwatch('shell');
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).not.toBe('');
  const origin = new URL(window.url()).origin;
  const visit = (await gw.focused()).textFocus;
  const key = visit.split('/').pop()!;

  // The tile lives in the scratch grid, and home is untouched.
  const scratch = await gw.getGrid(scratchGridID);
  const scratchShells = (scratch.tiles ?? []).filter((t) => t.kind === 'shell');
  expect(scratchShells, 'one ephemeral shell in the scratch grid').toHaveLength(1);
  expect(tileCount(await gw.getGrid(home.gridID)), 'home grid unchanged').toBe(
    tileCount(homeBefore),
  );

  // The title says the visit is ephemeral, and the name is the user's to
  // write like a placed tile's: ephemeral is retirement only.
  await expect.poll(async () => (await gw.barName()).label).toMatch(/ — ephemeral$/);
  await gw.clickBarName('right');
  const rename = window.locator('#gw-rename-input');
  await expect(rename).toBeVisible();
  await rename.fill('scratch-shell');
  await rename.press('Enter');
  await expect
    .poll(async () => ((await gw.getGrid(scratchGridID)).tiles ?? []).find((t) => t.kind === 'shell')?.altText ?? '')
    .toBe('scratch-shell');
  await expect.poll(async () => (await gw.barName()).label).toBe('scratch-shell — ephemeral');

  // attachShellRenderer falls back to the slower DOM renderer when WebGL2 is
  // unavailable, and Chromium can drop software WebGL out from under it. This
  // assertion makes that downgrade a suite failure.
  await expect
    .poll(() => window.evaluate(() => (window as any).__gridwellTest.shellRenderer()))
    .toBe('webgl');

  // Typing sends the keys through xterm to the PTY.
  await window.keyboard.type('echo ephemeral-shell-proof');
  await window.keyboard.press('Enter');
  // Waiting for echo's output line proves the keys crossed the PTY and came
  // back, with no wall-clock guess.
  await expect
    .poll(async () => {
      const t: string = await window.evaluate(() => (window as any).__gridwellTest.shellText());
      return t.split('\n').some((l) => l.includes('ephemeral-shell-proof') && !l.includes('echo '));
    }, { timeout: 10_000 })
    .toBe(true);

  // Ascending by crumb click sends the tile to the trash, its tmux running.
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  await expect
    .poll(async () => (await gw.getGrid(scratchGridID)).tiles?.length ?? 0, { timeout: 10_000 })
    .toBe(0);
  expect(tileCount(await gw.getGrid(home.gridID)), 'ascent left home unchanged').toBe(
    tileCount(homeBefore),
  );
  await expect
    .poll(async () => (await trashed(origin, visit))?.altText, { message: 'the closed visit is in the trash, its name kept', timeout: 10_000 })
    .toBe('scratch-shell');
  expect(tmuxRunning(nodeDir, key), 'the trashed shell keeps its tmux').toBe(true);

  // Reopened from the trash it is the same tmux, its output still on screen.
  await gw.clickSwatch('trash (home)');
  const month = ((await gw.getGrid((await gw.focused()).gridID)).tiles ?? []).find((t) => t.kind === 'door')!;
  await gw.descendCell(Number(month.x ?? 0), Number(month.y ?? 0));
  const row = (await trashed(origin, visit))!;
  await gw.descendCell(Number(row.x ?? 0), Number(row.y ?? 0));
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 15_000 }).toBe(visit);
  await expect.poll(async () => (await gw.barName()).label, { message: 'in the trash it is no visit' }).toBe('scratch-shell');
  await expect
    .poll(async () => {
      const t: string = await window.evaluate(() => (window as any).__gridwellTest.shellText());
      return t.split('\n').some((l) => l.includes('ephemeral-shell-proof') && !l.includes('echo '));
    }, { message: 'the reopened shell shows what the visit left', timeout: 10_000 })
    .toBe(true);
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  expect(await trashed(origin, visit), 'leaving it in the trash retires nothing').toBeTruthy();

  // Destroyed from the trash, the row and its tmux go.
  await gw.deleteTileCell(Number(row.x ?? 0), Number(row.y ?? 0));
  await expect.poll(() => tmuxRunning(nodeDir, key), { message: 'the destroy ended the tmux', timeout: 10_000 }).toBe(false);
  expect(await trashed(origin, visit), 'the destroy took the row').toBeUndefined();

  // Nothing on the notice row.
  const e = await window.evaluate(() => (window as any).__gridwellTest.errors());
  expect(e.notices, 'no error notices from the ephemeral shell round trip').toHaveLength(0);
});
