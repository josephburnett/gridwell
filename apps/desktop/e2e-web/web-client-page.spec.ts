import { expect } from '@playwright/test';
import { test } from './fixtures';
import { dumpViaAPI } from '../e2e/trace';

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

// In a browser there is no main to write a crash file, so the screen says
// the client stopped, and why, and the node's trace keeps the stack.
test('a client panic says so on screen and leaves its stack in the node trace', async ({ window, serve }) => {
  await window.evaluate(() => (window as any).__gridwellTest.panic());
  await expect(window.locator('#gw-boot.failed #gw-boot-msg')).toContainText('panic: e2e panic hook', { timeout: 10_000 });
  const stack = async () =>
    (await dumpViaAPI(serve.origin, serve.token))
      .filter((l) => l.src === 'client' && l.kind === 'exit')
      .sort((a, b) => Number(a.kv?.i) - Number(b.kv?.i))
      .map((l) => l.msg)
      .join('');
  await expect.poll(stack, { timeout: 10_000 }).toContain('panic: e2e panic hook');
  expect(await stack(), 'the Go stack').toContain('goroutine ');
});
