// Integration harness for the IPC contract. capture-harness calls the registry
// directly; this drives the whole path the wasm renderer uses, from
// window.gridwell.* through the preload bridge and the ipcMain handlers.
//
//   npm run build && xvfb-run -a electron dist/harness/bridge-harness.js
import { app, WebContentsView, Menu } from 'electron';
import * as path from 'node:path';
import { WebviewRegistry } from '../main/webviews';
import { registerWebviewIpc } from '../main/register';
import { hostWindow } from './host';

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
        // the wasm's bridgeCaps. The key must exist as a boolean; a misspelled
        // caps object falls into the full-feature imputation and claims a
        // native url-view half a host may not implement.
        const caps = window.gridwell.caps;
        if (!caps || typeof caps.liveUrl !== 'boolean') {
          console.log('BRIDGE_RESULT ' + JSON.stringify({ err: 'caps contract broken: ' + JSON.stringify(caps) }));
          return;
        }
        // focused:false is the renderer's verdict for a placement on a pane
        // that is not the focused one, such as a workspace restore. It must
        // reach the registry's entry across the real IPC seam.
        await window.gridwell.placeWebview({
          paneId: 'p1', tileId: 'u1/7',
          url: 'data:text/html,' + encodeURIComponent('<title>Inner</title><body style="margin:0;background:#2980b9">y</body>'),
          bounds: { x: 0, y: 0, width: 400, height: 300 },
          focused: false,
        });
        // The pump's pane set crosses the same seam, whole, and an empty set
        // is a set, not a missing one.
        await window.gridwell.setMirrored({ paneIds: ['p1', 'p2'] });
        await window.gridwell.setMirrored({ paneIds: [] });
        console.log('BRIDGE_PLACED');
        await new Promise(r => setTimeout(r, 1500));
        // A takeover hands the view to another pane, page and all, and a move
        // from a pane with no view is refused rather than dropped.
        await window.gridwell.moveWebview({
          fromPaneId: 'p1', toPaneId: 'w1:p1',
          bounds: { x: 20, y: 30, width: 300, height: 200 },
          durable: true, hidden: false, focused: true,
        });
        let refused = '';
        await window.gridwell.moveWebview({ fromPaneId: 'p9', toPaneId: 'p8', durable: true, hidden: false, focused: false })
          .catch((e) => { refused = String(e); });
        if (!refused.includes('no live view to move')) throw new Error('a move from an empty pane was not refused: ' + refused);
        console.log('BRIDGE_MOVED');
        await new Promise(r => setTimeout(r, 200));
        const f = await window.gridwell.removeWebview({ paneId: 'w1:p1' });
        console.log('BRIDGE_RESULT ' + JSON.stringify({ hasJpeg: f.jpegBase64.length > 0, title: f.title }));
      } catch (e) {
        console.log('BRIDGE_RESULT ' + JSON.stringify({ err: String(e) }));
      }
    })();
  </script>`);

app.whenReady().then(async () => {
  const win = await hostWindow().catch((e: unknown) => fail(String(e)));
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

  // The announce has to land before the menu is up, because once it is up an
  // item can run, and it would run in a pane that never took focus.
  // showContextMenu is the funnel both doors pass through. Menu.popup is
  // stubbed, because a native menu under xvfb would never be dismissed.
  const order: string[] = [];
  const realPopup = Menu.prototype.popup;
  Menu.prototype.popup = function stubPopup(this: Menu): void {
    order.push('popup');
  };
  const registry = new WebviewRegistry(win, {
    onContextMenu: (ev) => order.push(`focus:${ev.paneId}`),
  });
  const mirrored: string[][] = [];
  registerWebviewIpc(registry, root.webContents, win, (paneIds) => mirrored.push(paneIds));

  let placedWC = -1;
  root.webContents.on('console-message', (_e, _level, message) => {
    if (message === 'BRIDGE_MOVED') {
      const wc = registry.webContentsFor('w1:p1');
      if (!wc || wc.id !== placedWC) fail(`the move did not carry the placed view (webContents ${wc?.id} vs ${placedWC})`);
      if (registry.has('p1')) fail('the old pane still holds the moved view');
      const b = registry.viewBoundsFor('w1:p1');
      if (JSON.stringify(b) !== JSON.stringify({ x: 20, y: 30, width: 300, height: 200 })) {
        fail(`MoveArgs.bounds did not reach the view: ${JSON.stringify(b)}`);
      }
      if (registry.focusedFor('w1:p1') !== true) fail('MoveArgs.focused did not reach the entry');
      console.log('bridge ok: moveWebview carried the same view to w1:p1 with its bounds and focus');
      return;
    }
    if (message === 'BRIDGE_PLACED') {
      placedWC = registry.webContentsFor('p1')?.id ?? -1;
      // The entry must carry the renderer's verdict, because the steal guard
      // reads it from the first frame, before the next setHidden could correct
      // it and before addChildView and loadURL hand the widget OS focus.
      if (JSON.stringify(mirrored) !== JSON.stringify([['p1', 'p2'], []])) {
        fail(`setMirrored did not reach main as sent: ${JSON.stringify(mirrored)}`);
      }
      console.log('bridge ok: setMirrored carried both pane sets to main');

      const f = registry.focusedFor('p1');
      if (f !== false) fail(`PlaceArgs.focused did not reach the registry entry (focusedFor=${String(f)})`);
      console.log('bridge ok: PlaceArgs.focused=false reached the entry');

      registry.showMenu('p1');
      Menu.prototype.popup = realPopup;
      const got = order.join(',');
      if (got !== 'focus:p1,popup') {
        fail(`the context menu must announce its pane before it pops; got [${got}]`);
      }
      console.log('bridge ok: the context menu announced pane p1 before popping');
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
