import { test, expect } from './fixtures';

// Real-stack regression test for the reported bug: a plain right-click on a
// link inside a live URL tile did nothing (Electron's WebContentsView has no
// default context menu — webviews.ts must build one). This drives the ACTUAL
// production path: the real urlview-preload (which must let a plain right-click
// through), the real `context-menu` handler in webviews.ts, the real template
// builder, and the real clipboard.
//
// The canvas-only harness can't reach a WebContentsView (it's a separate
// webContents off the main page), so the test runs in the MAIN process via
// electronApp.evaluate: it places a real live view through the registry that
// index.ts exposes under GRIDWELL_E2E, sends a genuine right-click into that
// view's webContents, intercepts Menu.popup, and asserts the menu offers
// "Copy Link Address" which copies the link's href to the clipboard.
test('right-clicking a link in a live url view offers Copy Link Address', async ({ electronApp, window }) => {
  // `window` ensures the app finished booting (so the registry is exposed).
  await window.title();

  // The link's path slug doubles as a unique, URL-safe locator: it survives
  // encodeURIComponent verbatim, so we can find this exact view's webContents by
  // matching getURL().includes(marker) without colliding with the corner-control
  // view (also a data: URL, but without this slug).
  const marker = 'gwe2ectxtarget';
  const linkURL = `https://example.com/${marker}?q=1`;
  // A full-bleed anchor so a click anywhere in the view lands on the link.
  const html =
    `<!doctype html><meta charset=utf8>` +
    `<style>html,body{margin:0;height:100%}a{display:block;width:100vw;height:100vh}</style>` +
    `<a href="${linkURL}">link</a>`;
  const dataURL = 'data:text/html,' + encodeURIComponent(html);

  const result = await electronApp.evaluate(
    async ({ webContents, Menu, clipboard }, args) => {
      const reg = (globalThis as { __gwRegistry?: any }).__gwRegistry;
      if (!reg) throw new Error('registry not exposed (GRIDWELL_E2E not set?)');

      // Place a REAL live URL view (preload + context-menu handler wired by the
      // production code), at a fixed on-screen rect, with no plugin (shared
      // partition, no session hydrate / network).
      await reg.place('e2e-ctx', 1, 'e2e-obj-1', args.dataURL, { x: 0, y: 0, width: 800, height: 600 }, '');

      // place() returns once loadURL is kicked off; the view's getURL() is
      // empty until the load commits, so poll until our slug appears.
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

      // Intercept the menu instead of popping a native (blocking) menu.
      const origPopup = Menu.prototype.popup;
      let captured: any = null;
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      (Menu.prototype as any).popup = function (this: any) {
        captured = this;
        return undefined;
      };

      try {
        // A genuine right press+release: through the real preload (a plain
        // click is NOT a drag, so it is not suppressed) → Chromium emits
        // `context-menu` with the link's href → our handler builds + pops it.
        wc.focus();
        wc.sendInputEvent({ type: 'mouseDown', x: 100, y: 100, button: 'right', clickCount: 1 } as any);
        wc.sendInputEvent({ type: 'mouseUp', x: 100, y: 100, button: 'right', clickCount: 1 } as any);

        const deadline = Date.now() + 8000;
        while (!captured && Date.now() < deadline) {
          await new Promise<void>((res) => setTimeout(res, 50));
        }
        if (!captured) throw new Error('context menu was never built (right-click produced no menu)');

        const labels: string[] = captured.items
          .map((i: any) => i.label)
          .filter((l: string) => l);

        // Exercise the actual copy action and read it back off the clipboard.
        clipboard.writeText('');
        const copyItem = captured.items.find((i: any) => i.label === 'Copy Link Address');
        if (copyItem && typeof copyItem.click === 'function') copyItem.click();

        return { labels, clipboard: clipboard.readText() };
      } finally {
        (Menu.prototype as any).popup = origPopup;
        await reg.remove('e2e-ctx');
      }
    },
    { dataURL, marker },
  );

  expect(result.labels, 'menu should offer Copy Link Address').toContain('Copy Link Address');
  expect(result.labels, 'menu should offer Open Link').toContain('Open Link');
  expect(result.clipboard, 'Copy Link Address must copy the link href').toBe(linkURL);
});
