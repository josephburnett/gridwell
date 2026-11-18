import { expect } from '@playwright/test';
import { test } from './fixtures';

// A drop the page does not cancel is a navigation, so a file or a link dropped
// on the canvas would replace the client. The event here is synthetic, which
// Chromium never navigates on, so the spec reads the verdict the page gave it.
test('a drop on the page is cancelled, so it cannot replace the client', async ({ window }) => {
  const cancelled = await window.evaluate(() =>
    ['dragover', 'drop'].map((type) => {
      const ev = new DragEvent(type, { bubbles: true, cancelable: true, dataTransfer: new DataTransfer() });
      document.getElementById('canvas')!.dispatchEvent(ev);
      return `${type}:${ev.defaultPrevented}`;
    }),
  );
  expect(cancelled).toEqual(['dragover:true', 'drop:true']);
});

// In a browser there is no main to write a crash file, so the screen alone
// says the client stopped, and why.
test('a client panic says so on screen', async ({ window }) => {
  await window.evaluate(() => (window as any).__gridwellTest.panic());
  await expect(window.locator('#gw-boot.failed #gw-boot-msg')).toContainText('panic: e2e panic hook', { timeout: 10_000 });
});
