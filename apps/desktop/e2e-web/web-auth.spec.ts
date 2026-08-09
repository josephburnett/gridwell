import { test as base, expect, Page } from '@playwright/test';
import { spawn, ChildProcess } from 'node:child_process';
import * as net from 'node:net';
import * as path from 'node:path';
import * as fs from 'node:fs';
import { seedHome } from '../e2e/fixtures';

// The web-UI password gate (server.yaml `password:`), driven from a real
// browser against the real server: the login page fronts everything, a wrong
// password re-prompts, the right one sets the cookie and the wasm client
// boots, and the cookie keeps working across a reload (it is checked against
// the CURRENT password, so no re-prompt until the password changes). The
// no-password suites (web-core & co.) pin the open behavior — this file is
// the only one whose server sets a password.

const REPO_ROOT = path.resolve(__dirname, '..', '..', '..');

function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const srv = net.createServer();
    srv.on('error', reject);
    srv.listen(0, '127.0.0.1', () => {
      const port = (srv.address() as net.AddressInfo).port;
      srv.close(() => resolve(port));
    });
  });
}

const PASSWORD = 'e2e-secret';

type Fixtures = {
  serve: { origin: string };
};

const test = base.extend<Fixtures>({
  serve: async ({}, use) => {
    const home = seedHome();
    // The password rides server.yaml like a user would set it.
    const cfgPath = path.join(home, 'server.yaml');
    fs.appendFileSync(cfgPath, `password: "${PASSWORD}"\n`);
    const port = await freePort();
    const origin = `http://127.0.0.1:${port}`;
    const child: ChildProcess = spawn(
      path.join(REPO_ROOT, 'gridwell'),
      ['serve', '--bind', `127.0.0.1:${port}`, '--static', path.join(REPO_ROOT, 'web')],
      { env: { ...process.env, GRIDWELL_HOME: home }, stdio: ['ignore', 'pipe', 'pipe'] },
    );
    let output = '';
    child.stdout!.on('data', (d) => (output += d));
    child.stderr!.on('data', (d) => (output += d));

    // Readiness: the gate answers (401 with the login page) — res.ok would
    // never come true unauthenticated, which is the point of this suite.
    const deadline = Date.now() + 15_000;
    for (;;) {
      try {
        const res = await fetch(origin + '/');
        if (res.status === 401) break;
      } catch {
        // not up yet
      }
      if (Date.now() > deadline) {
        child.kill('SIGKILL');
        throw new Error(`gridwell serve did not become ready on ${origin}:\n${output}`);
      }
      await new Promise((r) => setTimeout(r, 100));
    }

    await use({ origin });

    child.kill('SIGTERM');
    await new Promise<void>((resolve) => {
      const hard = setTimeout(() => {
        child.kill('SIGKILL');
        resolve();
      }, 3_000);
      child.once('exit', () => {
        clearTimeout(hard);
        resolve();
      });
    });
    fs.rmSync(home, { recursive: true, force: true });
  },
});

async function expectBooted(page: Page): Promise<void> {
  await page.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
}

test('the password gate: prompt, wrong password, login, cookie survives reload', async ({
  serve,
  page,
}) => {
  // Every address serves the login form until authenticated.
  await page.goto(serve.origin + '/?e2e=1');
  const field = page.locator('input[name=password]');
  await expect(field, 'unauthenticated visit must show the login form').toBeVisible();

  // A wrong password re-prompts with an error and does NOT boot the app.
  await field.fill('not-it');
  await page.locator('button[type=submit]').click();
  await expect(page.locator('.err')).toHaveText('wrong password');

  // The right password sets the cookie and lands home; the wasm client
  // boots normally behind the gate.
  await page.locator('input[name=password]').fill(PASSWORD);
  await page.locator('button[type=submit]').click();
  await page.waitForURL(serve.origin + '/');
  await page.goto(serve.origin + '/?e2e=1');
  await expectBooted(page);

  // The cookie is the durable credential: a fresh navigation re-enters the
  // app directly, no prompt.
  await page.goto(serve.origin + '/?e2e=1');
  await expectBooted(page);
  await expect(page.locator('input[name=password]')).toHaveCount(0);
});

export { expect };
