import * as fs from 'node:fs';
import * as path from 'node:path';
import { test, expect, homePassword, loginToken } from './fixtures';
import { dumpViaAPI } from './trace';

// The node's trace ring dies with the node and the client's with the canvas,
// so when either dies main writes what it last heard to a crash file in the
// node's dumps directory. These kill the real processes, because the crash
// file is the one record that has to survive them.

function crashFiles(home: string): string[] {
  const dir = path.join(home, 'dumps');
  if (!fs.existsSync(dir)) return [];
  return fs
    .readdirSync(dir)
    .filter((n) => /^crash-\d{8}-\d{6}\.txt$/.test(n))
    .map((n) => fs.readFileSync(path.join(dir, n), 'utf8'));
}

// The renderer's console line rides into both the trace and the crash tail.
const PROBE = 'crash-file-probe';

test('a killed node leaves a crash file holding its last output and no secret', async ({
  electronApp,
  window,
  home,
}) => {
  const origin = new URL(window.url()).origin;
  const password = homePassword(home);
  const token = await loginToken(origin, password);
  await window.evaluate((m) => console.error(m), PROBE);
  await expect
    .poll(async () => (await dumpViaAPI(origin, token)).some((l) => l.kind === 'renderer-log' && l.msg.includes(PROBE)), {
      timeout: 20_000,
      message: 'the renderer line reached the node trace',
    })
    .toBe(true);

  const pid = await electronApp.evaluate(() => (globalThis as { __gwSidecarPid?: number }).__gwSidecarPid ?? null);
  expect(pid).not.toBeNull();
  process.kill(pid!, 'SIGKILL');

  await expect.poll(() => crashFiles(home).length, { timeout: 10_000 }).toBe(1);
  const text = crashFiles(home)[0];
  expect(text).toMatch(/^gridwell desktop crash: sidecar-exit\n/);
  expect(text).toContain('signal: SIGKILL');
  expect(text, 'the node output tail').toContain('gridwell: serving on');
  expect(text, 'the renderer tail').toContain(PROBE);
  expect(text.includes(password), 'the web password is in the crash file').toBe(false);
  expect(text.includes(token), 'the auth token is in the crash file').toBe(false);
});

test('a dead canvas is traced and leaves a crash file', async ({ electronApp, window, home }) => {
  const origin = new URL(window.url()).origin;
  const token = await loginToken(origin, homePassword(home));

  await electronApp.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows()[0].webContents.forcefullyCrashRenderer());

  await expect.poll(() => crashFiles(home).length, { timeout: 10_000 }).toBe(1);
  expect(crashFiles(home)[0]).toMatch(/^gridwell desktop crash: renderer-gone\n/);
  // The node outlives the canvas, so main's record of the death reaches it.
  await expect
    .poll(async () => (await dumpViaAPI(origin, token)).find((l) => l.src === 'window' && l.kind === 'gone')?.kv ?? null, {
      timeout: 20_000,
    })
    .toMatchObject({ reason: expect.any(String), exitCode: expect.any(String) });
});
