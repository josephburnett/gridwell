import { test, expect } from './fixtures';

// Sites that gate on browser version block a live url tile when Chromium's
// default UA carries an `Electron/<ver>` token. index.ts strips that token, and
// the app's own `<AppName>/<ver>` token, from app.userAgentFallback at boot, so a
// live view must report a plain Chrome UA with no Electron in it.
//
// The canvas-only harness cannot see a WebContentsView, so this runs in the main
// process: place a real live view through the GRIDWELL_E2E registry, then read
// navigator.userAgent out of that view's own webContents, which is the UA the
// network and page JS see.
test('a live url view reports a Chrome UA with no Electron token', async ({ electronApp, window }) => {
  await window.title(); // boot finished: the registry and userAgentFallback are set

  const marker = 'gwe2euatarget';
  const html =
    `<!doctype html><meta charset=utf8><title>${marker}</title><body>ua test</body>`;
  const dataURL = 'data:text/html,' + encodeURIComponent(html);

  const ua = await electronApp.evaluate(
    async ({ webContents }, args) => {
      const reg = (globalThis as { __gwRegistry?: any }).__gwRegistry;
      if (!reg) throw new Error('registry not exposed (GRIDWELL_E2E not set?)');

      await reg.place('e2e-ua', 1, args.dataURL, { x: 0, y: 0, width: 400, height: 300 });
      try {
        let wc: any = null;
        const findDeadline = Date.now() + 8000;
        while (!wc && Date.now() < findDeadline) {
          wc = webContents.getAllWebContents().find((w: any) => w.getURL().includes(args.marker));
          if (!wc) await new Promise<void>((res) => setTimeout(res, 50));
        }
        if (!wc) throw new Error('live view webContents not found');
        if (wc.isLoadingMainFrame()) {
          await new Promise<void>((res) => wc.once('did-stop-loading', () => res()));
        }
        // navigator.userAgent is what the page reads, and it reflects the same
        // fallback the network request header uses.
        return (await wc.executeJavaScript('navigator.userAgent')) as string;
      } finally {
        await reg.remove('e2e-ua');
      }
    },
    { dataURL, marker },
  );

  expect(ua, 'live view UA must not contain the Electron token').not.toMatch(/Electron\//);
  expect(ua, 'live view UA must still read as real Chrome').toMatch(/Chrome\//);
});
