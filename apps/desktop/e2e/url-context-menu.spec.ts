import { test, expect } from './fixtures';

// Real-stack regression tests for right-click context menus on live URL tiles.
//
// The canvas-only harness can't reach a WebContentsView (it's a separate
// webContents off the main page), so the tests run in the MAIN process via
// electronApp.evaluate: they place a real live view through the registry that
// index.ts exposes under GRIDWELL_E2E, send genuine mouse events into that
// view's webContents, intercept Menu.popup, and assert the menu items.


test('right-clicking a link in a live url view offers Copy Link Address', async ({ electronApp, window }) => {
  // `window` ensures the app finished booting (so the registry is exposed).
  await window.title();

  // The link's path slug doubles as a unique, URL-safe locator: it survives
  // encodeURIComponent verbatim, so we can find this exact view's webContents
  // by matching getURL().includes(marker).
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

// Regression guard for mechanism B of issue #33: a jittery right-click that
// moves a few pixels (exceeding the 4 px distance threshold) but is released
// quickly must still produce the context menu. Before the fix, 5 px of movement
// — however fast — would arm the pane-gesture path and suppress contextmenu.
// After the fix the classification requires BOTH distance AND a hold of at least
// RIGHT_DRAG_TIME_MS (200 ms); a rapid press+mousemove+release has near-zero
// duration so it still reaches the context-menu handler.
//
// Why was this not caught? The original url-context-menu.spec.ts sends a
// zero-movement right-click (mouseDown + mouseUp at the same coords, no
// mouseMove) — the 4 px suppression path was never exercised by any test.
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

      await reg.place('e2e-jitter', 1, 'e2e-obj-jitter', args.dataURL, { x: 0, y: 0, width: 800, height: 600 }, '');

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
        // Right-click WITH 5 px of jitter: exceeds the 4 px distance threshold
        // but the button is released immediately (events fired in rapid succession
        // → duration well under 200 ms). Before the fix this would arm the pane
        // gesture and suppress contextmenu. After the fix the time gate blocks
        // the drag classification so the menu fires normally.
        wc.sendInputEvent({ type: 'mouseDown', x: 100, y: 100, button: 'right', clickCount: 1 } as any);
        // Move 5 px — past the 4 px threshold but fast (same JS task, ~0 ms delta).
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
