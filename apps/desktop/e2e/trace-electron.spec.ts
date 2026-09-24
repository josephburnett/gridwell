import * as fs from 'node:fs';
import { test, expect, homePassword, loginToken } from './fixtures';

// The main process's trace crosses three seams the Electron gates cannot see
// together: a WebContentsView event becomes a record, the record is posted to
// the web door on the app's own cookie, and the node's ring keeps it under the
// one origin a sender may claim. A unit test on either side would pass with the
// door unreachable and the app none the wiser, because a trace that fails says
// nothing by design.

interface Line {
  origin: string;
  src: string;
  kind: string;
  msg: string;
  cid?: string;
  ct?: number;
  seq?: number;
  t?: number;
}

// The dump is a reading: it writes the ring to a file and clears nothing, so a
// poll may take it as often as it likes.
async function dumpLines(origin: string, token: string): Promise<Line[]> {
  const res = await fetch(origin + '/trace/dump', {
    method: 'POST',
    headers: { Cookie: `gridwell_auth=${token}` },
  });
  if (!res.ok) throw new Error(`POST /trace/dump = ${res.status} ${await res.text()}`);
  const { path } = (await res.json()) as { path: string; records: number };
  return fs
    .readFileSync(path, 'utf8')
    .split('\n')
    .filter((l) => l !== '')
    .map((l) => JSON.parse(l) as Line);
}

test('a live url tile puts the main process into the node trace', async ({
  electronApp,
  window,
  home,
  gw,
}) => {
  const origin = new URL(window.url()).origin;
  const token = await loginToken(origin, homePassword(home));

  await gw.enterPlugin('home');
  const wcBefore = await electronApp.evaluate(
    ({ webContents }) => webContents.getAllWebContents().length,
  );
  await gw.clickPaletteSwatch('url');
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  await window.fill('#gw-url-input', 'https://example.com/trace-electron');
  await window.locator('#gw-url-form').evaluate((f: HTMLFormElement) => f.requestSubmit());
  await gw.waitIdle();
  await expect
    .poll(() => electronApp.evaluate(({ webContents }) => webContents.getAllWebContents().length), {
      timeout: 15_000,
    })
    .toBeGreaterThan(wcBefore);

  // The batch waits out the flush window, so the dump is polled rather than
  // taken once.
  let views: Line[] = [];
  await expect
    .poll(
      async () => {
        views = (await dumpLines(origin, token)).filter(
          (l) => l.origin === 'electron' && l.src === 'webviews',
        );
        return views.length;
      },
      { timeout: 20_000 },
    )
    .toBeGreaterThan(0);

  const create = views.find((l) => l.kind === 'create');
  expect(create, `no create record among ${JSON.stringify(views)}`).toBeTruthy();
  expect(create!.msg).toContain('trace-electron');
  // The node stamps the order and keeps the emitter's own clock beside it, so
  // the desktop's records can be read against the node's.
  expect(create!.cid).toMatch(/^[a-z][a-z0-9]{6}$/);
  expect(create!.seq).toBeGreaterThan(0);
  expect(create!.ct).toBeGreaterThan(0);

  // The sidecar's own lifecycle rides the same ring, under its own src.
  const all = await dumpLines(origin, token);
  expect(
    all.filter((l) => l.origin === 'electron' && l.src === 'sidecar' && l.kind === 'ready'),
  ).not.toHaveLength(0);
});
