// Integration harness for the IPC contract. Where capture-harness calls the
// registry directly, this drives the whole path the wasm renderer uses: a real
// renderer page calls window.gridwell.* from the preload bridge, which invokes
// the ipcMain handlers, which drive the registry. It covers preload exposure,
// the IPC round trip, and capture and freeze.
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
        // The caps declaration is a string contract between the preload and
        // the wasm's bridgeCaps: the KEY must exist as a
        // boolean, or the wasm silently degrades — a misspelled caps object
        // falls into the legacy full-feature imputation, claiming a native
        // url-view half a host may not implement.
        const caps = window.gridwell.caps;
        if (!caps || typeof caps.liveUrl !== 'boolean') {
          console.log('BRIDGE_RESULT ' + JSON.stringify({ err: 'caps contract broken: ' + JSON.stringify(caps) }));
          return;
        }
        // focused:false is the renderer's verdict for a placement on a pane
        // that is not the focused one — a workspace restore, an ascent
        // re-engaging every content pane, a promote. It must reach the
        // registry's entry across the real IPC seam, not be guessed there.
        await window.gridwell.placeWebview({
          paneId: 'p1', tileId: 'u1/7',
          url: 'data:text/html,' + encodeURIComponent('<title>Inner</title><body style="margin:0;background:#2980b9">y</body>'),
          bounds: { x: 0, y: 0, width: 400, height: 300 },
          focused: false,
        });
        console.log('BRIDGE_PLACED');
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
    if (message === 'BRIDGE_PLACED') {
      // PlaceArgs.focused crossed the seam: the entry must carry the
      // renderer's verdict, since the focus-steal guard reads it from the
      // first frame — before the renderer's next setHidden could correct it,
      // and before addChildView and loadURL hand the widget OS focus.
      const f = registry.focusedFor('p1');
      if (f !== false) fail(`PlaceArgs.focused did not reach the registry entry (focusedFor=${String(f)})`);
      console.log('bridge ok: PlaceArgs.focused=false reached the entry');
      return;
    }
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
