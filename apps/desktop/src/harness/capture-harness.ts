// L3 integration harness for Phase 2. Boots a real Electron main process,
// creates a window + WebviewRegistry, hosts a real WebContentsView on a
// data: URL, then asserts that (a) capturePage yields non-empty JPEG bytes
// and (b) a nav event fires. Run with:
//
//   npm run build && xvfb-run -a electron dist/harness/capture-harness.js
//
// Prints "HARNESS PASS" / "HARNESS FAIL: ..." and exits with 0/1.
import * as http from 'http';
import { app, BaseWindow } from 'electron';
import { WebviewRegistry } from '../main/webviews';
import type { NavEvent } from '../main/ipc';

const DATA_URL =
  'data:text/html,' +
  encodeURIComponent('<title>HarnessTitle</title><body style="margin:0;background:#c0392b">x</body>');

function fail(msg: string): never {
  console.error('HARNESS FAIL:', msg);
  app.exit(1);
  throw new Error(msg); // unreachable; satisfies never
}

// sidecarStub serves GET/PUT /session/<uuid> from an in-memory map (the
// same shape session-harness uses), so the dead-view scenario below can
// assert the dehydrate PUT actually arrived.
function sidecarStub(blobs: Map<string, Buffer>): Promise<string> {
  return new Promise((resolve) => {
    const srv = http.createServer((req, res) => {
      const uuid = (req.url ?? '').replace('/session/', '');
      if (req.method === 'PUT') {
        const chunks: Buffer[] = [];
        req.on('data', (c) => chunks.push(c));
        req.on('end', () => {
          blobs.set(uuid, Buffer.concat(chunks));
          res.writeHead(200).end();
        });
        return;
      }
      res.writeHead(200).end(blobs.get(uuid) ?? Buffer.alloc(0));
    });
    srv.listen(0, '127.0.0.1', () => {
      const a = srv.address() as { port: number };
      resolve(`http://127.0.0.1:${a.port}`);
    });
  });
}

async function waitForNonEmptyCapture(
  registry: WebviewRegistry,
  paneId: string,
  timeoutMs: number,
): Promise<string> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const jpeg = await registry.capture(paneId);
    if (jpeg.length > 0) return jpeg;
    await new Promise((r) => setTimeout(r, 100));
  }
  return '';
}

app.whenReady().then(async () => {
  const navEvents: NavEvent[] = [];
  const win = new BaseWindow({ width: 800, height: 600, show: true });
  const registry = new WebviewRegistry(win, '', { onNav: (ev) => navEvents.push(ev) });

  registry.place('pane1', 42, 'obj-harness', DATA_URL, { x: 0, y: 0, width: 800, height: 600 }, '');

  const jpeg = await waitForNonEmptyCapture(registry, 'pane1', 6000);
  if (jpeg.length === 0) fail('capturePage produced no frame within 6s');

  // base64 of a JPEG starts with "/9j/".
  if (!jpeg.startsWith('/9j/')) fail(`capture is not JPEG base64 (got prefix ${jpeg.slice(0, 8)})`);
  console.log(`capture ok: ${jpeg.length} base64 chars`);

  // Give nav events a beat to arrive (title update).
  await new Promise((r) => setTimeout(r, 500));
  if (navEvents.length === 0) fail('no nav event fired');
  const last = navEvents[navEvents.length - 1];
  if (last.tileId !== 42) fail(`nav tileId wrong: ${last.tileId}`);
  console.log(`nav ok: title=${JSON.stringify(last.title)} url-prefix=${last.url.slice(0, 16)}`);

  // remove() returns a freeze snapshot.
  const freeze = await registry.remove('pane1');
  if (freeze.jpegBase64.length === 0) fail('freeze snapshot empty');
  if (registry.has('pane1')) fail('pane still registered after remove');
  console.log(`freeze ok: ${freeze.jpegBase64.length} base64 chars, title=${JSON.stringify(freeze.title)}`);

  // ── a dead view still saves its session on remove() ────────────────────
  // The crashed-tab ascent: every view-bound read in remove() throws once
  // the webContents is gone, but the SESSION outlives the view, so the
  // flush + dehydrate must still run — it is the only write path that
  // persists this run's logins/drafts to the plugin DB. (Ordering
  // regression: the preview reads used to run first and their throw skipped
  // the dehydrate entirely; the loss surfaced only as "preview not
  // updated".)
  const blobs = new Map<string, Buffer>();
  const origin = await sidecarStub(blobs);
  const errors: string[] = [];
  const reg2 = new WebviewRegistry(win, origin, { onError: (e2) => errors.push(e2.message) });
  await reg2.place('pane2', 43, 'obj-dead', DATA_URL, { x: 0, y: 0, width: 400, height: 300 }, 'dead-view-uuid');
  if ((await waitForNonEmptyCapture(reg2, 'pane2', 6000)).length === 0) {
    fail('dead-view scenario: view produced no frame within 6s');
  }
  // Destroy the renderer out from under the registry — the crashed-tab shape.
  (reg2 as unknown as { entries: Map<string, { view: { webContents: { close(): void } } }> })
    .entries.get('pane2')!
    .view.webContents.close();
  await new Promise((r) => setTimeout(r, 300));
  const deadFreeze = await reg2.remove('pane2');
  if (deadFreeze.jpegBase64 !== '') fail('dead view yielded a non-empty freeze');
  const putDeadline = Date.now() + 5000;
  while (!blobs.has('dead-view-uuid') && Date.now() < putDeadline) {
    await new Promise((r) => setTimeout(r, 100));
  }
  if (!blobs.has('dead-view-uuid')) {
    fail("dead-view remove() skipped the session dehydrate — this run's logins were dropped");
  }
  console.log('dead-view dehydrate ok: session PUT arrived despite the crashed view');

  console.log('HARNESS PASS');
  app.exit(0);
});
