import { test as base, expect, Page } from '@playwright/test';
import * as path from 'node:path';
import * as fs from 'node:fs';
import { seedHome } from '../e2e/fixtures';
import { removeHome } from '../e2e/homes';
import { spawnServe, stopServe, freePort } from './fixtures';

// The web password gate, held in the minted <home>/web-password file, from a
// real browser against the real server: the login page fronts everything, the
// right password sets the cookie, and the cookie keeps working across a reload
// until the password changes. Every suite's server is gated; the others start
// from the banner token, and this one alone drives the login form.


const PASSWORD = 'e2e-secret';

type Fixtures = {
  serve: { origin: string };
};

const test = base.extend<Fixtures>({
  serve: async ({}, use) => {
    const home = seedHome();
    // serve mints a password when the file is absent, so the door is never
    // open; a user who wants a memorable one writes the file, as here.
    fs.writeFileSync(path.join(home, 'web-password'), PASSWORD + '\n', { mode: 0o600 });
    const served = await spawnServe(home, await freePort());
    await use({ origin: served.origin });
    await stopServe(served.child);
    removeHome(home);
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

  await field.fill('not-it');
  await page.locator('button[type=submit]').click();
  await expect(page.locator('.err')).toHaveText('wrong password');

  await page.locator('input[name=password]').fill(PASSWORD);
  await page.locator('button[type=submit]').click();
  await page.waitForURL(serve.origin + '/');
  await page.goto(serve.origin + '/?e2e=1');
  await expectBooted(page);

  // The cookie is the durable credential.
  await page.goto(serve.origin + '/?e2e=1');
  await expectBooted(page);
  await expect(page.locator('input[name=password]')).toHaveCount(0);
});

export { expect };
