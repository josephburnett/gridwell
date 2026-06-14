// L3 integration harness for the Phase 3 IPC contract. Unlike capture-
// harness (which calls the registry directly), this drives the *full* path
// the wasm renderer uses: a real renderer page calls window.gridwell.* from
// the preload bridge, which invokes ipcMain handlers, which drive the
// registry. Proves preload exposure + IPC round-trip + capture/freeze.
//
//   npm run build && xvfb-run -a electron dist/harness/bridge-harness.js
import { app, BaseWindow, WebContentsView } from 'electron';
import * as path from 'node:path';
import { WebviewRegistry } from '../main/webviews';
import { registerWebviewIpc } from '../main/register';

function fail(msg: string): never {
  console.error('HARNESS FAIL:', msg);
  app.exit(1);
  throw new Error(msg);
}

const PAGE =
  'data:text/html,' +
  encodeURIComponent(`<title>host</title><script>
    (async () => {
      try {
        if (!window.gridwell) { console.log('BRIDGE_RESULT ' + JSON.stringify({err:'no window.gridwell'})); return; }
        await window.gridwell.placeWebview({
          paneId: 'p1', tileId: 7, objectId: 'obj-bridge',
          url: 'data:text/html,' + encodeURIComponent('<title>Inner</title><body style="margin:0;background:#2980b9">y</body>'),
          bounds: { x: 0, y: 0, width: 400, height: 300 },
        });
        await new Promise(r => setTimeout(r, 1500));
        const f = await window.gridwell.removeWebview({ paneId: 'p1' });
        console.log('BRIDGE_RESULT ' + JSON.stringify({ hasJpeg: f.jpegBase64.length > 0, title: f.title }));
      } catch (e) {
        console.log('BRIDGE_RESULT ' + JSON.stringify({ err: String(e) }));
      }
    })();
  </script>`);

app.whenReady().then(() => {
  const win = new BaseWindow({ width: 800, height: 600, show: true });
  const root = new WebContentsView({
    webPreferences: {
      preload: path.join(__dirname, '..', 'preload', 'preload.js'),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: false,
    },
  });
  win.contentView.addChildView(root);
  root.setBounds({ x: 0, y: 0, width: 800, height: 600 });

  const registry = new WebviewRegistry(win);
  registerWebviewIpc(registry, root.webContents, win);

  root.webContents.on('console-message', (_e, _level, message) => {
    if (!message.startsWith('BRIDGE_RESULT ')) return;
    const payload = JSON.parse(message.slice('BRIDGE_RESULT '.length));
    if (payload.err) fail(`renderer reported error: ${payload.err}`);
    if (!payload.hasJpeg) fail('freeze returned no JPEG over the IPC bridge');
    if (payload.title !== 'Inner') fail(`freeze title wrong over bridge: ${JSON.stringify(payload.title)}`);
    console.log(`bridge ok: freeze carried JPEG + title=${JSON.stringify(payload.title)} via preload→ipcMain→registry`);
    console.log('HARNESS PASS');
    app.exit(0);
  });

  void root.webContents.loadURL(PAGE);
  setTimeout(() => fail('no BRIDGE_RESULT within 8s'), 8000);
});
