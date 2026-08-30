import { test, expect } from './fixtures';

// Right-click context menus on live url tiles, on the real stack.
//
// A WebContentsView is a separate webContents off the main page, so the
// canvas-only harness cannot reach it and these tests run in the main process
// through electronApp.evaluate: they place a real live view through the registry
// index.ts exposes under GRIDWELL_E2E, send genuine mouse events into that
// view's webContents, intercept Menu.popup, and assert the menu items.


test('right-clicking a link in a live url view offers Copy Link Address', async ({ electronApp, window }) => {
  // `window` ensures the app finished booting, so the registry is exposed.
  await window.title();

  // The link's path slug doubles as a unique, url-safe locator: it survives
  // encodeURIComponent verbatim, so this exact view's webContents is found by
  // matching getURL().includes(marker).
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

      // Place a real live url view, with the preload and context-menu handler
      // wired by the production code, at a fixed on-screen rect on the shared
      // partition.
      await reg.place('e2e-ctx', 1, args.dataURL, { x: 0, y: 0, width: 800, height: 600 });

      // place() returns once loadURL is kicked off, and the view's getURL() is
      // empty until the load commits, so poll until the slug appears.
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

      // Intercept the menu instead of popping a native, blocking one.
      const origPopup = Menu.prototype.popup;
      let captured: any = null;
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      (Menu.prototype as any).popup = function (this: any) {
        captured = this;
        return undefined;
      };

      try {
        // A genuine right press and release. It goes through the real preload,
        // which does not suppress it because a plain click is not a drag, so
        // Chromium emits `context-menu` with the link's href and the handler
        // builds and pops the menu.
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

        // Exercise the real copy action and read it back off the clipboard.
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

// A jittery right-click that moves a few pixels, past the 4px distance
// threshold, but is released quickly must still produce the context menu.
// Distance alone would arm the pane-gesture path and suppress contextmenu, so
// the classification requires both distance and a hold of at least
// RIGHT_DRAG_TIME_MS, 200ms. A rapid press, move, and release has near-zero
// duration and still reaches the context-menu handler.
//
// The test above sends a zero-movement right-click, so it never exercises the
// suppression path at all; this one does.
test('a jittery right-click (5px movement, fast release) still produces the context menu', async ({
  electronApp,
  window,
}) => {
  await window.title();

  const marker = 'gwe2ejitter';
  const linkURL = `https://example.com/${marker}?q=1`;
  const html =
    `<!doctype html><meta charset=utf8>` +
    `<style>html,body{margin:0;height:100%}a{display:block;width:100vw;height:100vh}</style>` +
    `<a href="${linkURL}">link</a>`;
  const dataURL = 'data:text/html,' + encodeURIComponent(html);

  const result = await electronApp.evaluate(
    async ({ webContents, Menu, clipboard }, args) => {
      const reg = (globalThis as { __gwRegistry?: any }).__gwRegistry;
      if (!reg) throw new Error('registry not exposed (GRIDWELL_E2E not set?)');

      await reg.place('e2e-jitter', 1, args.dataURL, { x: 0, y: 0, width: 800, height: 600 });

      let wc: any = null;
      const findDeadline = Date.now() + 8_000;
      while (!wc && Date.now() < findDeadline) {
        wc = webContents.getAllWebContents().find((w: any) => w.getURL().includes(args.marker));
        if (!wc) await new Promise<void>((res) => setTimeout(res, 50));
      }
      if (!wc) throw new Error('live view webContents not found');
      if (wc.isLoadingMainFrame()) {
        await new Promise<void>((res) => wc.once('did-stop-loading', () => res()));
      }

      const origPopup = Menu.prototype.popup;
      let captured: any = null;
      (Menu.prototype as any).popup = function (this: any) {
        captured = this;
        return undefined;
      };

      try {
        wc.focus();
        // A right-click with 5px of jitter: past the 4px distance threshold, but
        // the button is released immediately, with the events fired in rapid
        // succession so the duration is well under 200ms. The time gate blocks
        // the drag classification and the menu fires normally.
        wc.sendInputEvent({ type: 'mouseDown', x: 100, y: 100, button: 'right', clickCount: 1 } as any);
        // Move 5px: past the 4px threshold, but in the same JS task, so the
        // elapsed time is near zero.
        wc.sendInputEvent({ type: 'mouseMove', x: 105, y: 100, buttons: 2 } as any);
        wc.sendInputEvent({ type: 'mouseUp', x: 105, y: 100, button: 'right', clickCount: 1 } as any);

        const deadline = Date.now() + 8_000;
        while (!captured && Date.now() < deadline) {
          await new Promise<void>((res) => setTimeout(res, 50));
        }
        if (!captured)
          throw new Error(
            'context menu was not produced for jittery right-click — right-drag may have suppressed contextmenu',
          );

        const labels: string[] = captured.items.map((i: any) => i.label).filter((l: string) => l);

        clipboard.writeText('');
        const copyItem = captured.items.find((i: any) => i.label === 'Copy Link Address');
        if (copyItem && typeof copyItem.click === 'function') copyItem.click();

        return { labels, clipboard: clipboard.readText() };
      } finally {
        (Menu.prototype as any).popup = origPopup;
        await reg.remove('e2e-jitter');
      }
    },
    { dataURL, marker },
  );

  expect(
    result.labels,
    'jittery right-click must still offer Copy Link Address (not be swallowed as a drag)',
  ).toContain('Copy Link Address');
  expect(result.clipboard, 'Copy Link Address must copy the link href').toBe(linkURL);
});
