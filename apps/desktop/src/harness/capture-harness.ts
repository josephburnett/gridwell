// L3 integration harness for Phase 2. Boots a real Electron main process,
// creates a window + WebviewRegistry, hosts a real WebContentsView on a
// data: URL, then asserts that (a) capturePage yields non-empty JPEG bytes
// and (b) a nav event fires. Run with:
//
//   npm run build && xvfb-run -a electron dist/harness/capture-harness.js --no-sandbox
//
// Prints "HARNESS PASS" / "HARNESS FAIL: ..." and exits with 0/1.
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
  const registry = new WebviewRegistry(win, { onNav: (ev) => navEvents.push(ev) });

  registry.place('pane1', 42, 'obj-harness', DATA_URL, { x: 0, y: 0, width: 800, height: 600 });

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

  console.log('HARNESS PASS');
  app.exit(0);
});
